-- Only executed by this isolated Compose project's first MySQL initialization.
-- Test suites never reset refund_demo; acceptance uses a separate schema.
CREATE DATABASE IF NOT EXISTS refund_test CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
CREATE DATABASE IF NOT EXISTS refund_acceptance CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
GRANT ALL PRIVILEGES ON refund_test.* TO 'demo'@'%';
GRANT ALL PRIVILEGES ON refund_acceptance.* TO 'demo'@'%';
