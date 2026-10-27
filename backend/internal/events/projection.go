package events

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// The seen set intentionally has no expiry: duplicate delivery, even following
// a long outage/replay, must not insert the same event twice. Only the latest
// ActivityLimit payloads are retained. Production would need an explicit
// retention/rebuild policy for this auxiliary idempotency index.
// Check key types before any mutation: Redis Lua is atomic but not rollbackable.
var projectionScript = redis.NewScript(`
local seenType = redis.call('TYPE', KEYS[1]).ok
local listType = redis.call('TYPE', KEYS[2]).ok
if (seenType ~= 'none' and seenType ~= 'set') or
   (listType ~= 'none' and listType ~= 'list') then
  return redis.error_reply('activity projection key has incompatible type')
end
if redis.call('SISMEMBER', KEYS[1], ARGV[1]) == 1 then
  return 0
end
redis.call('LPUSH', KEYS[2], ARGV[2])
redis.call('LTRIM', KEYS[2], 0, tonumber(ARGV[3]) - 1)
redis.call('SADD', KEYS[1], ARGV[1])
return 1
`)

var limiterScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

func (s *Service) activityKeys(merchantID string) (seen, list string) {
	// The hash tag keeps both Lua keys in one slot if Redis Cluster is introduced.
	merchant := base64.RawURLEncoding.EncodeToString([]byte(merchantID))
	base := s.redisPrefix() + "merchant:{" + merchant + "}:"
	return base + "seen", base + "activity"
}

func (s *Service) project(ctx context.Context, payload []byte) error {
	if s.redis == nil {
		return errors.New("redis activity projection unavailable")
	}
	event, err := decodeEvent(payload)
	if err != nil {
		return err
	}
	seen, list := s.activityKeys(event.MerchantID)
	opCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return projectionScript.Run(opCtx, s.redis, []string{seen, list},
		event.EventID, payload, ActivityLimit).Err()
}

// Activity returns an empty non-nil slice and available=false on Redis failure
// or corrupt projection data. This is not a refund failure. SQL must still be
// used for all business/detail reads. The caller supplies authenticated merchant.
func (s *Service) Activity(ctx context.Context, merchantID string) ([]Event, bool) {
	items := make([]Event, 0)
	if s.redis == nil {
		return items, false
	}
	_, list := s.activityKeys(merchantID)
	opCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	values, err := s.redis.LRange(opCtx, list, 0, ActivityLimit-1).Result()
	if err != nil {
		s.log.Warn("activity unavailable; refund data remains in MySQL", "error", err)
		return items, false
	}
	for _, value := range values {
		event, err := decodeEvent([]byte(value))
		if err != nil || event.MerchantID != merchantID {
			s.log.Error("activity projection corrupt; refusing mixed/corrupt activity", "merchant", merchantID)
			return make([]Event, 0), false
		}
		items = append(items, event)
	}
	return items, true
}

func (s *Service) limiterKey(identity string, now time.Time) string {
	digest := sha256.Sum256([]byte(identity))
	return s.redisPrefix() + "rate:" + hex.EncodeToString(digest[:]) + ":" + strconv.FormatInt(now.Unix()/60, 10)
}

// Limiter applies 300 requests per UTC fixed minute to an authenticated identity.
// It deliberately fails open on Redis failure. It is not a funds-safety or
// authentication boundary, and must never replace SQL idempotency/constraints.
// available indicates whether the rate-limit check actually ran.
func (s *Service) Limiter(ctx context.Context, identity string) (allowed, available bool) {
	if s.redis == nil {
		s.log.Warn("rate limiter unavailable; allowing request")
		return true, false
	}
	now := s.now()
	key := s.limiterKey(identity, now)
	// Keep the completed minute briefly for concurrent in-flight requests.
	ttl := 65*time.Second - time.Duration(now.Unix()%60)*time.Second
	opCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	count, err := limiterScript.Run(opCtx, s.redis, []string{key}, ttl.Milliseconds()).Int64()
	if err != nil {
		s.log.Warn("rate limiter failed open", "error", err)
		return true, false
	}
	return count <= RateLimit, true
}

// marshalEvent is used by tests/integration helpers to ensure the same encoding
// validates when it crosses the Kafka boundary.
func marshalEvent(event Event) ([]byte, error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if _, err := decodeEvent(payload); err != nil {
		return nil, fmt.Errorf("encode event: %w", err)
	}
	return payload, nil
}
