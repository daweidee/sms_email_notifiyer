package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"sort"
	"strings"
	"time"

	"hello/internal/config"
)

// Sender 负责从数据库拉取邮件并发送，支持多通道按 used 优先级依次尝试。
type Sender struct {
	channels    []emailChannel
	defaultFrom string
	db          *sql.DB
}

// emailChannel 单条邮件发送通道（SMTP 或 REST 二选一），按 used 降序尝试。
type emailChannel struct {
	used int
	name string
	smtp *smtpChannelConfig
	rest *restChannelConfig
}

type smtpChannelConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string // 发件人显示名称，收件端显示为「名称」而非仅邮箱
}

type restChannelConfig struct {
	Endpoint       string
	APIKey         string
	APIUser        string
	From           string
	FromName       string
	TimeoutSeconds int
}

// isEmailChannelUsed 为 true 时表示该通道启用（used >= 0）；used: -1 表示不使用，不加入发送列表。
func isEmailChannelUsed(used int) bool {
	return used >= 0
}

// formatFromDisplay 格式化为 "显示名称 <邮箱>"，收件端显示名称而非仅邮箱；fromName 为空则只返回 email。
func formatFromDisplay(email, fromName string) string {
	email = strings.TrimSpace(email)
	fromName = strings.TrimSpace(fromName)
	if fromName == "" {
		return email
	}
	// 名称中含逗号、分号、双引号等时需用双引号括起来
	if strings.ContainsAny(fromName, ",\";<>[]\\") {
		esc := strings.ReplaceAll(fromName, "\\", "\\\\")
		esc = strings.ReplaceAll(esc, "\"", "\\\"")
		return "\"" + esc + "\" <" + email + ">"
	}
	return fromName + " <" + email + ">"
}

func NewSender(cfg config.EmailConfig, db *sql.DB) *Sender {
	var channels []emailChannel
	var defaultFrom string

	// engagelab_smtp：used < 0 时不启用
	if cfg.EngagelabSmtp != nil && isEmailChannelUsed(cfg.EngagelabSmtp.Used) &&
		cfg.EngagelabSmtp.Host != "" && cfg.EngagelabSmtp.Port > 0 {
		channels = append(channels, emailChannel{
			used: cfg.EngagelabSmtp.Used,
			name: "engagelab_smtp",
			smtp: &smtpChannelConfig{
				Host:     cfg.EngagelabSmtp.Host,
				Port:     cfg.EngagelabSmtp.Port,
				Username: cfg.EngagelabSmtp.Username,
				Password: cfg.EngagelabSmtp.Password,
				From:     cfg.EngagelabSmtp.From,
				FromName: cfg.EngagelabSmtp.FromName,
			},
		})
		if defaultFrom == "" && cfg.EngagelabSmtp.From != "" {
			defaultFrom = cfg.EngagelabSmtp.From
		}
	} else if cfg.EngagelabSmtp != nil && cfg.EngagelabSmtp.Used < 0 {
		slog.Info("邮件通道已跳过(used=-1)", slog.String("channel", "engagelab_smtp"), slog.Int("used", cfg.EngagelabSmtp.Used))
	}
	if cfg.EngagelabRest != nil && isEmailChannelUsed(cfg.EngagelabRest.Used) &&
		cfg.EngagelabRest.Endpoint != "" {
		channels = append(channels, emailChannel{
			used: cfg.EngagelabRest.Used,
			name: "engagelab_rest",
			rest: &restChannelConfig{
				Endpoint:       cfg.EngagelabRest.Endpoint,
				APIKey:         cfg.EngagelabRest.APIKey,
				APIUser:        cfg.EngagelabRest.APIUser,
				From:           cfg.EngagelabRest.From,
				FromName:       cfg.EngagelabRest.FromName,
				TimeoutSeconds: cfg.EngagelabRest.TimeoutSeconds,
			},
		})
		if defaultFrom == "" && cfg.EngagelabRest.From != "" {
			defaultFrom = cfg.EngagelabRest.From
		}
	} else if cfg.EngagelabRest != nil && cfg.EngagelabRest.Used < 0 {
		slog.Info("邮件通道已跳过(used=-1)", slog.String("channel", "engagelab_rest"), slog.Int("used", cfg.EngagelabRest.Used))
	}
	if cfg.GodaddySmtp != nil && isEmailChannelUsed(cfg.GodaddySmtp.Used) &&
		cfg.GodaddySmtp.Host != "" && cfg.GodaddySmtp.Port > 0 {
		channels = append(channels, emailChannel{
			used: cfg.GodaddySmtp.Used,
			name: "godaddy_smtp",
			smtp: &smtpChannelConfig{
				Host:     cfg.GodaddySmtp.Host,
				Port:     cfg.GodaddySmtp.Port,
				Username: cfg.GodaddySmtp.Username,
				Password: cfg.GodaddySmtp.Password,
				From:     cfg.GodaddySmtp.From,
				FromName: cfg.GodaddySmtp.FromName,
			},
		})
		if defaultFrom == "" && cfg.GodaddySmtp.From != "" {
			defaultFrom = cfg.GodaddySmtp.From
		}
	} else if cfg.GodaddySmtp != nil && cfg.GodaddySmtp.Used < 0 {
		slog.Info("邮件通道已跳过(used=-1)", slog.String("channel", "godaddy_smtp"), slog.Int("used", cfg.GodaddySmtp.Used))
	}

	// used 数值越大优先级越高，降序排列
	sort.Slice(channels, func(i, j int) bool { return channels[i].used > channels[j].used })

	// 若仍未设置默认发件人，从第一个通道取
	if defaultFrom == "" && len(channels) > 0 {
		if c := channels[0].smtp; c != nil {
			defaultFrom = c.From
		}
		if c := channels[0].rest; c != nil && defaultFrom == "" {
			defaultFrom = c.From
		}
	}

	return &Sender{
		channels:    channels,
		defaultFrom: defaultFrom,
		db:          db,
	}
}

// ProcessPending 查找 status=0 的待发送邮件并处理。
// 返回本次成功处理的邮件数量。
func ProcessStatusToString(status int8) string {
	switch status {
	case 0:
		return "未发送"
	case 1:
		return "已发送"
	case 2:
		return "发送失败"
	default:
		return fmt.Sprintf("未知状态(%d)", status)
	}
}

// ProcessPending 查找 status=0 的待发送邮件并处理。
// 返回本次成功处理的邮件数量。
func (s *Sender) ProcessPending(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(
		ctx,
		"SELECT id, `from`, `to`, bcc, cc, subject, content, attach_id FROM email_gateway_send WHERE status = 0",
	)
	if err != nil {
		return 0, fmt.Errorf("查询待发送邮件失败: %w", err)
	}
	defer rows.Close()

	type mailRow struct {
		id       int64
		from     string
		to       string
		bcc      string
		cc       string
		subject  string
		content  string
		attachID int64
	}

	var list []mailRow
	for rows.Next() {
		var m mailRow
		if err := rows.Scan(&m.id, &m.from, &m.to, &m.bcc, &m.cc, &m.subject, &m.content, &m.attachID); err != nil {
			return 0, fmt.Errorf("扫描待发送邮件行失败: %w", err)
		}
		list = append(list, m)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("遍历待发送邮件结果集失败: %w", err)
	}

	successCount := 0
	for _, m := range list {
		if err := s.handleOne(ctx, m); err != nil {
			slog.Error("发送邮件失败", slog.Int64("id", m.id), slog.Any("err", err))
		} else {
			successCount++
		}
	}

	return successCount, nil
}

// SendByID 发送指定 ID 的邮件记录。
func (s *Sender) SendByID(ctx context.Context, id int64) error {
	row := s.db.QueryRowContext(
		ctx,
		"SELECT id, `from`, `to`, bcc, cc, subject, content, attach_id FROM email_gateway_send WHERE id = ?",
		id,
	)

	var m struct {
		id       int64
		from     string
		to       string
		bcc      string
		cc       string
		subject  string
		content  string
		attachID int64
	}

	if err := row.Scan(&m.id, &m.from, &m.to, &m.bcc, &m.cc, &m.subject, &m.content, &m.attachID); err != nil {
		return fmt.Errorf("查询邮件(id=%d)失败: %w", id, err)
	}

	return s.handleOne(ctx, m)
}

func (s *Sender) handleOne(ctx context.Context, m struct {
	id       int64
	from     string
	to       string
	bcc      string
	cc       string
	subject  string
	content  string
	attachID int64
}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// 乐观锁：仅当 status=0 时改为 3（发送中），保证同一条记录同一时刻只会被发送一次
	const statusSending = 3
	res, err := s.db.ExecContext(ctx,
		"UPDATE email_gateway_send SET status = ?, update_time = ? WHERE id = ? AND status = 0",
		statusSending, time.Now().Unix(), m.id,
	)
	if err != nil {
		return fmt.Errorf("占用发送记录失败: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// 已被其他协程/请求占用或已发送/已失败，不再重复发送
		var current int8
		_ = s.db.QueryRowContext(ctx, "SELECT status FROM email_gateway_send WHERE id = ?", m.id).Scan(&current)
		if current == 1 {
			return nil // 已发送，幂等返回成功
		}
		if current == 2 {
			return fmt.Errorf("邮件(id=%d)已标记为发送失败", m.id)
		}
		return nil // status=3 表示其他协程正在发送，避免重复发
	}

	from := m.from
	if from == "" {
		from = s.defaultFrom
	}

	toList := splitAndTrim(m.to)
	ccList := splitAndTrim(m.cc)
	bccList := splitAndTrim(m.bcc)

	if len(toList) == 0 {
		return fmt.Errorf("邮件(id=%d)没有任何收件人", m.id)
	}

	var attachments [][]byte
	if m.attachID != 0 {
		content, err := s.loadAttachment(ctx, m.attachID)
		if err != nil {
			return fmt.Errorf("加载附件失败: %w", err)
		}
		attachments = append(attachments, content)
	}

	// 按 used 优先级从高到低依次尝试，第一个成功即结束；全部失败则返回最后错误
	var sendErr error
	for _, ch := range s.channels {
		if ch.smtp != nil {
			sendErr = s.sendViaSMTPWith(ch.smtp, from, toList, ccList, bccList, m.subject, m.content, attachments)
		} else {
			sendErr = s.sendViaRESTWith(ctx, ch.rest, from, toList, ccList, bccList, m.subject, m.content, attachments)
		}
		if sendErr == nil {
			break
		}
		slog.Warn("邮件通道发送失败，尝试下一通道",
			slog.Int64("id", m.id),
			slog.String("channel", ch.name),
			slog.Any("err", sendErr))
	}

	if sendErr != nil {
		_ = s.updateStatus(ctx, m.id, 2)
		slog.Error("邮件发送失败",
			slog.Int64("id", m.id),
			slog.String("status", "failed"),
			slog.String("from", from),
			slog.Any("to", toList),
			slog.Any("err", sendErr))
		return sendErr
	}

	if err := s.updateStatus(ctx, m.id, 1); err != nil {
		return fmt.Errorf("更新邮件状态失败: %w", err)
	}
	slog.Info("邮件发送成功",
		slog.Int64("id", m.id),
		slog.String("status", "sent"),
		slog.String("from", from),
		slog.Any("to", toList))
	return nil
}

func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func (s *Sender) loadAttachment(ctx context.Context, id int64) ([]byte, error) {
	var encoded string
	err := s.db.QueryRowContext(ctx, "SELECT content FROM email_gateway_attach WHERE id = ?", id).Scan(&encoded)
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(encoded)
}

func (s *Sender) updateStatus(ctx context.Context, id int64, status int8) error {
	_, err := s.db.ExecContext(
		ctx,
		"UPDATE email_gateway_send SET status = ?, update_time = ? WHERE id = ?",
		status,
		time.Now().Unix(),
		id,
	)
	return err
}

// sendViaSMTPWith 使用指定 SMTP 通道配置发送邮件，连接与发送均带超时，避免长时间无返回。
func (s *Sender) sendViaSMTPWith(cfg *smtpChannelConfig, from string, to, cc, bcc []string, subject, body string, _ [][]byte) error {
	if cfg == nil {
		return fmt.Errorf("SMTP 通道配置为空")
	}
	const smtpTimeout = 30 * time.Second
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	host := cfg.Host
	allTo := append(append([]string{}, to...), cc...)
	allTo = append(allTo, bcc...)
	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, host)
	fromHeader := formatFromDisplay(from, cfg.FromName)
	headers := map[string]string{
		"From":         fromHeader,
		"To":           strings.Join(to, ","),
		"Subject":      subject,
		"Content-Type": "text/html; charset=UTF-8",
	}
	if len(cc) > 0 {
		headers["Cc"] = strings.Join(cc, ",")
	}
	var msgBuilder strings.Builder
	for k, v := range headers {
		msgBuilder.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	msgBuilder.WriteString("\r\n")
	msgBuilder.WriteString(body)
	msg := []byte(msgBuilder.String())

	var conn net.Conn
	var err error
	if cfg.Port == 465 {
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: smtpTimeout}, "tcp", addr, &tls.Config{ServerName: host})
	} else {
		conn, err = net.DialTimeout("tcp", addr, smtpTimeout)
	}
	if err != nil {
		return fmt.Errorf("SMTP 连接失败(%s): %w", addr, err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("SMTP NewClient 失败: %w", err)
	}
	defer client.Close()

	if err = conn.SetDeadline(time.Now().Add(smtpTimeout)); err != nil {
		return fmt.Errorf("SMTP 设置截止时间失败: %w", err)
	}
	// 587 等端口通常需要 STARTTLS
	if cfg.Port == 587 {
		if err = client.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("SMTP StartTLS 失败: %w", err)
		}
		if err = conn.SetDeadline(time.Now().Add(smtpTimeout)); err != nil {
			return fmt.Errorf("SMTP 设置截止时间失败: %w", err)
		}
	}
	if err = client.Auth(auth); err != nil {
		return fmt.Errorf("SMTP Auth 失败: %w", err)
	}
	if err = client.Mail(from); err != nil {
		return fmt.Errorf("SMTP Mail 失败: %w", err)
	}
	for _, rcp := range allTo {
		if err = client.Rcpt(rcp); err != nil {
			return fmt.Errorf("SMTP Rcpt 失败: %w", err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP Data 失败: %w", err)
	}
	if _, err = w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("SMTP 写入内容失败: %w", err)
	}
	if err = w.Close(); err != nil {
		return fmt.Errorf("SMTP Data 结束失败: %w", err)
	}
	return client.Quit()
}

// sendViaRESTWith 使用指定 REST 通道配置发送邮件。
// EngageLab: POST .../v1/mail/send, Header: Authorization: Basic base64(api_user:api_key)
func (s *Sender) sendViaRESTWith(ctx context.Context, cfg *restChannelConfig, from string, to, cc, bcc []string, subject, body string, attachments [][]byte) error {
	if cfg == nil || cfg.Endpoint == "" {
		return fmt.Errorf("REST 通道配置为空或 Endpoint 未配置")
	}
	type RestRequest struct {
		From string   `json:"from"`
		To   []string `json:"to"`
		Body struct {
			Subject string `json:"subject"`
			Content struct {
				HTML string `json:"html,omitempty"`
				Text string `json:"text,omitempty"`
			} `json:"content"`
		} `json:"body"`
	}
	fromHeader := formatFromDisplay(from, cfg.FromName)
	reqBody := RestRequest{From: fromHeader, To: to}
	reqBody.Body.Subject = subject
	reqBody.Body.Content.HTML = body
	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("构建 REST 请求 JSON 失败: %w", err)
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	httpClient := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("创建 REST 请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIUser != "" && cfg.APIKey != "" {
		token := base64.StdEncoding.EncodeToString([]byte(cfg.APIUser + ":" + cfg.APIKey))
		req.Header.Set("Authorization", "Basic "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("调用 REST API 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("REST API 返回非成功状态码: %d", resp.StatusCode)
	}
	return nil
}

