# eng_tools 邮件 & 短信服务

`eng_tools` 是一个基于 Go 实现的轻量级邮件 / 短信发送与查询服务，支持：

- 通过数据库 `email_gateway_send` 拉取待发送邮件，按配置的邮件通道（EngageLab SMTP / REST）按优先级发送，失败自动切换下一通道
- 提供 HTTP 接口发送邮件 `/send/email`
- 查询邮件投递状态 `/email/delivery`（调用 EngageLab 投递回应 API）
- 通过多个短信渠道发送短信 `/send/sms`，支持：
  - EngageLab SMS API（`sms.engagelab_sms`）
  - 梦网 MXT 单发 / 群发（`sms.mxt_sms`），多通道失败自动切换

## 1. 编译与运行

```bash
cd /Users/hypergo/GoProjects/eng_tools

# 本地运行
go run . -c config.yaml

# 构建 Linux 可执行文件
GOOS=linux GOARCH=amd64 go build -o eng_tools .
./eng_tools -c config.yaml
```

## 2. 配置文件说明（`config.yaml`）

关键配置段落示例与说明。

### 2.1 日志与服务

```yaml
log:
  level: "info"   # debug / info / warn / error

server:
  host: "0.0.0.0"
  port: 8080
```

### 2.2 数据库

```yaml
db:
  # MySQL DSN，可包含 timeout、loc、parseTime、allowOldPasswords 等
  dsn: "user:password@tcp(host:port)/dbname?timeout=10s&loc=Local&parseTime=true&allowOldPasswords=1"
  # 启动时 Ping 超时（秒）
  connect_timeout_seconds: 5
  # 最大打开连接数
  max_conn: 100
  # 最大空闲连接数
  max_idle: 100
```

- **db.dsn**: MySQL 连接串，需能访问表：`email_gateway_send`、`email_gateway_attach`、`email_gateway_black`、`smsagent_send`。
- **db.max_conn / max_idle**: 连接池大小，设为 0 时不修改默认值。

### 2.3 邮件（多通道，按 used 优先级 + 失败回退）

```yaml
email:
  # used: -1 不使用；1、2… 表示优先级，数值越大越优先；发送失败时依次尝试下一通道
  engagelab_smtp:
    used: 1
    host: "smtp.engagelab.cc"
    port: 465
    username: "smtp_user@example.com"
    password: "your_smtp_password"
    from: "noreply@example.com"

  engagelab_rest:
    used: 2
    endpoint: "http://email.api.engagelab.cc/v1/mail/send"
    api_key: "your_email_api_key"
    api_user: "noreplycoc"
    from: "noreplycoc@mail.coc.exchange"
    timeout_seconds: 10
  
  # 也可以配置其他 SMTP 通道（例如 GoDaddy），字段含义与 engagelab_smtp 一致：
  # godaddy_smtp:
  #   used: 1
  #   host: "smtpout.secureserver.net"
  #   port: 465
  #   username: "mail.example.com"
  #   password: "your_password"
  #   from: "mail.example.com"
  #   from_name: "No Reply"
```

- 邮件通道：`engagelab_smtp`、`godaddy_smtp`（或其他 SMTP）、`engagelab_rest` 可同时配置；**used 数值越大越优先**，先尝试高优先级通道，失败则自动尝试下一通道。
- **used: -1** 表示该通道不启用。
- HTTP 接口 `/send/email` 的默认发件人取自配置（优先 `engagelab_rest.from`，否则 `engagelab_smtp.from`）。

### 2.4 短信（多通道，按 used 优先级 + 失败回退）

```yaml
sms:
  engagelab_sms:
    used: 1
    endpoint: "https://smsapi.engagelab.com/v1/messages"
    dev_key: "your_dev_key"
    dev_secret: "your_dev_secret"
    timeout_seconds: 15
    proxy_name: "engagelab"

  mxt_sms:
    used: 1
    submit_url: "http://116.62.212.142/msg/HttpSendSM"
    batch_submit_url: "http://116.62.212.142/msg/HttpBatchSendSM"
    account: "your_account"
    pswd: "your_pswd"
    needstatus: true
    product: ""
    timeout_seconds: 15
    proxy_name: "mxt"
```

- 短信通道：`engagelab_sms`（EngageLab）、`mxt_sms`（梦网）均在 `sms` 下配置；**used 数值越大越优先**，失败自动切换下一通道。
- **used: -1** 表示该通道不启用。
- `proxy_name` 会写入 `smsagent_send.proxy_name`，用于区分渠道。

---

## 3. 邮件发送接口

### 3.1 发送邮件 `/send/email`

**请求方法**: `POST`  
**URL**: `/send/email`  
**用途**: 写入一条 `email_gateway_send` 记录，并立即发送。

请求体示例：

```bash
curl -X POST "http://127.0.0.1:8080/send/email" \
  -H "Content-Type: application/json" \
  -d '{
    "to": ["user1@example.com", "user2@example.com"],
    "subject": "测试邮件",
    "content": "<b>Hello</b> world"
  }'
```

返回示例：

```json
{
  "id": 123,
  "status": "sent"
}
```

发送逻辑：

- 将 `to` 列表合并为逗号分隔字符串写入 `email_gateway_send.to`。
- 按配置的邮件通道（`engagelab_smtp` / `engagelab_rest`）**used 从高到低**依次尝试发送，某一通道成功即结束；全部失败则标记为发送失败。
- 成功则将 `status` 更新为 `1`（已发送），失败为 `2`（发送失败）。
- 发件人 `from` 由配置文件提供（HTTP 接口不接收 from 参数）。

### 3.2 查询邮件投递回应 `/email/delivery`

接口基于 EngageLab 投递回应 API：[`/v1/email_status`](https://www.engagelab.com/zh_CN/docs/email/rest-api/delivery-response)。

**请求方法**: `GET`  
**URL**: `/email/delivery?to=<email>&send_date=YYYY-MM-DD`  

参数：

- `to` (必填): 收件人邮箱地址。
- `send_date` (可选): 发送日期（`YYYY-MM-DD`），不填则默认当天。

示例：

```bash
curl "http://127.0.0.1:8080/email/delivery?to=user@example.com&send_date=2026-03-11"
```

返回精简结果示例：

```json
{
  "api_user": "noreplycoc",
  "email": "user@example.com",
  "status": 4,
  "status_desc": "Invalid Email"
}
```

日志中会输出完整的 EngageLab 投递回应字段，便于排查：

- `email`, `email_id`, `api_user`, `status`, `status_desc`, `sub_status`, `sub_status_desc`, `request_time`, `update_time`, `response_message`

---

## 4. 短信发送接口

接口统一为 `/sms/send`，内部会根据配置自动选择 EngageLab 或梦网 MXT，失败时自动切换下一通道（按 `used` 优先级）。

### 4.1 请求格式

**请求方法**: `POST`  
**URL**: `/sms/send`

请求体示例（通用）：

```bash
curl -X POST "http://127.0.0.1:8080/sms/send" \
  -H "Content-Type: application/json" \
  -d '{
    "to": [
      "+8618701235678",
      "+8618700000000"
    ],
    "template": {
      "id": "notification-template",
      "params": {
        "content": "您的验证码为 039487，5 分钟内有效。"
      }
    }
  }'
```

字段说明：

- `to`: 手机号数组，支持 1 个或多个。
- `template.id`:
  - 对 EngageLab 通道必填（模板 ID）。
  - 对 MXT 通道可忽略。
- `template.params.content`:
  - 对 MXT 通道必填，作为短信正文内容。
  - 对 EngageLab 通道可按模板变量使用。

### 4.2 EngageLab SMS 通道（`sms.engagelab_sms`）

参考文档: [EngageLab SMS API 短信发送](https://www.engagelab.com/zh_CN/docs/NEWSMS/REST-API/API-SMS-Sending)。

当 **`sms.engagelab_sms` 存在且 used >= 0、endpoint / dev_key / dev_secret 已配置** 时，该通道参与发送：

- 使用 `engagelab_sms.endpoint`（如 `https://smsapi.engagelab.com/v1/messages`）。
- HTTP Basic 认证：`Authorization: Basic base64(dev_key:dev_secret)`。
- 请求体与文档一致：`to`、`template.id`、`template.params` 等。

---

### 4.3 梦网 MXT 单发 / 群发通道（`sms.mxt_sms`）

参考文档：

- 单发: [ID=92](https://www.mxtong.com/OtherViewkaf05.asp?ID=92)
- 群发: [ID=93](https://www.mxtong.com/OtherViewkaf05.asp?ID=93)

当 **`sms.mxt_sms` 存在且 used >= 0、submit_url / account / pswd 已配置** 时，该通道参与发送：

- **单发**（`len(to) == 1`）：使用 `mxt_sms.submit_url`（如 `HttpSendSM`）。
- **群发**（`len(to) > 1`）：优先使用 `mxt_sms.batch_submit_url`（如 `HttpBatchSendSM`）；未配置时由 `submit_url` 推导（将 `HttpSendSM` 替换为 `HttpBatchSendSM`）。

请求参数（内部拼接）：

- `account`、`pswd`、`mobile`（to 逗号连接）、`msg`（来自 `template.params["content"]`）
- `needstatus`：来自 `mxt_sms.needstatus`
- `product`：可选，来自 `mxt_sms.product`
- `resptype`: `json`

响应解析：

- 首行：`resptime,respstatus`；第二行：`msgid`。
- `respstatus == "0"` 视为受理成功。

### 4.4 多通道与失败重试

邮件与短信均支持多通道、按 **used 降序** 依次尝试：

- **邮件**：`engagelab_smtp`、`engagelab_rest`，used 大的先试，失败则下一通道。
- **短信**：`engagelab_sms`、`mxt_sms`，used 大的先试，失败则下一通道。

短信发送流程：

1. 按 used 从高到低调用通道。
2. 若返回成功（如 EngageLab 的 `code == 0` 且 `AcceptedCount > 0` 或 `PlanID != ""`），则采用该结果并结束。
3. 若失败，记录警告日志并尝试下一通道。
4. 全部失败时，返回最后一次失败信息。

无论成功或失败，都会写入 `smsagent_send` 表：`to_number`、`proxy_name`（通道标识）、`response`、`status`（1=成功，0=失败）。

---

## 5. 日志

日志使用 Go 标准库 `log/slog`，根据 `log.level` 控制输出级别。关键日志包括：

- 配置检查、数据库连接、HTTP 服务启动与优雅退出。
- 邮件发送成功/失败、邮件投递查询结果。
- 短信各通道发送结果、多通道失败切换过程。

示例（短信查询投递）：

```text
time=... level=INFO msg=查询投递记录 api_user=noreplycoc email=futreone@outlook.com status=4 status_desc="Invalid Email"
```

---

## 6. 表结构（SQL）

SQL 文件位于 `sql/` 目录：

- `email_gateway_send.sql`
- `email_gateway_attach.sql`
- `email_gateway_black.sql`
- `smsagent_send.sql`

请在目标数据库中执行这些建表语句后再运行服务。  
如需扩展字段或索引，请保证与当前代码访问字段兼容。 

