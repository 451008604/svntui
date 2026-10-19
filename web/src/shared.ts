export const actions: Record<string, string> = {
  commit: "提交",
  add: "添加到版本控制",
  delete: "删除",
  move: "移动 / 重命名",
  revert: "还原",
  update: "更新",
  changelist: "变更分组",
  resolve: "解决冲突",
  cleanup: "清理",
  restore: "恢复 SafePoint",
};
export const time = (value: string) =>
  new Date(value).toLocaleString("zh-CN", { hour12: false });
