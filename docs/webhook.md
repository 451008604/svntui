# Webhook 协议

Webhook 使用 `POST application/json`。每次事件有唯一 `event_id`，接收方应按该字段去重。

事件类型：

- `update.succeeded`
- `update.no_change`
- `update.skipped`
- `update.failed`
- `update.paused`
- `update.resumed`

示例：

```json
{
  "event_id": "event-93c0...",
  "type": "update.skipped",
  "time": "2026-08-30T10:00:00Z",
  "task": {
    "id": "task-a71e...",
    "name": "trunk-update",
    "working_copy": "/srv/work/trunk",
    "repository_uuid": "...",
    "branch_url": "https://svn.example/repo/trunk",
    "status": "skipped"
  },
  "run": {
    "status": "skipped",
    "reason": "working copy contains versioned local changes"
  }
}
```

配置密钥后会附加：

```text
X-SVNTUI-Timestamp: 2026-08-30T10:00:00Z
X-SVNTUI-Signature: sha256=<hex>
```

签名输入是以下字节的 HMAC-SHA256：

```text
timestamp + "." + raw_request_body
```

只有 HTTP 2xx 视为成功。HTTP 429、5xx 和网络错误最多尝试三次（含首次）；其他 4xx 不重试。响应正文不会写入任务状态或操作日志。
