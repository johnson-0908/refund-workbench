import type { App, Component } from 'vue'
import Alert from 'ant-design-vue/es/alert'
import Button from 'ant-design-vue/es/button/button'
import ConfigProvider from 'ant-design-vue/es/config-provider'
import Empty from 'ant-design-vue/es/empty'
import Input from 'ant-design-vue/es/input/Input'
import Textarea from 'ant-design-vue/es/input/TextArea'
import Modal from 'ant-design-vue/es/modal/Modal'
import Segmented from 'ant-design-vue/es/segmented'
import Select from 'ant-design-vue/es/select'
import Spin from 'ant-design-vue/es/spin'
import Table from 'ant-design-vue/es/table/Table'
import Tag from 'ant-design-vue/es/tag'

// Register only template components. Avoid plugin side effects that retain unused
// Input.Password/Search, Modal.confirm/useModal, and Table.Column/Summary APIs.
// The package version is locked; real-component tests use this same registry.
export const uiComponents: Record<string, Component> = {
  AAlert: Alert, AButton: Button, AConfigProvider: ConfigProvider, AEmpty: Empty,
  AInput: Input, ATextarea: Textarea, AModal: Modal, ASegmented: Segmented,
  ASelect: Select, ASelectOption: Select.Option, ASpin: Spin, ATable: Table, ATag: Tag,
}

export function installUI(app: App) {
  for (const [name, component] of Object.entries(uiComponents)) app.component(name, component)
}
