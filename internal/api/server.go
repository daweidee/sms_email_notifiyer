package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"hello/internal/config"
	"hello/internal/email"
)

// Server 封装 HTTP 服务依赖。
type Server struct {
	sender      *email.Sender
	db          *sql.DB
	defaultFrom string
	restCfg     config.RestConfig
	smsCfg      config.SMSConfig
}

func NewServer(sender *email.Sender, db *sql.DB, defaultFrom string, restCfg config.RestConfig, smsCfg config.SMSConfig) *Server {
	return &Server{
		sender:      sender,
		db:          db,
		defaultFrom: defaultFrom,
		restCfg:     restCfg,
		smsCfg:      smsCfg,
	}
}

// sendEmailRequest 是发送邮件接口的请求体。
type sendEmailRequest struct {
	To      []string `json:"to"`      // 1 个或多个收件人
	Subject string   `json:"subject"` // 主题
	Content string   `json:"content"` // 邮件内容（text 或 HTML）
}

type sendEmailResponse struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// deliveryResponse 投递回应信息，字段与 EngageLab /v1/email_status 接口一致。
// 文档: https://www.engagelab.com/zh_CN/docs/email/rest-api/delivery-response
type deliveryResponse struct {
	Email           string `json:"email"`            // 收件人地址
	EmailID         string `json:"email_id"`         // 调用发送接口成功返回的 emailId
	APIUser         string `json:"api_user"`         // api_user 名称
	Status          int    `json:"status"`           // 投递状态：请求中 18，送达 1，软退信 5，无效邮件 4
	StatusDesc      string `json:"status_desc"`      // 投递状态描述
	SubStatus       int    `json:"sub_status"`       // 无效/软退信子类 401-509
	SubStatusDesc   string `json:"sub_status_desc"`  // 无效或软退信子类描述
	RequestTime     string `json:"request_time"`     // 请求时间
	UpdateTime      string `json:"update_time"`      // 状态更新时间
	ResponseMessage string `json:"response_message"` // 发送结果消息
}

// engagelabEmailStatusResult EngageLab /v1/email_status 接口返回结构。
type engagelabEmailStatusResult struct {
	Result []deliveryResponse `json:"result"`
	Total  string             `json:"total"`
	Count  int                `json:"count"`
}

// deliverySimpleResponse 返回给调用方的精简投递回应信息。
type deliverySimpleResponse struct {
	APIUser    string `json:"api_user"`
	Email      string `json:"email"`
	Status     int    `json:"status"`
	StatusDesc string `json:"status_desc"`
}

// sendSMSRequest 发送短信请求体，与 EngageLab 文档一致。
// 文档: https://www.engagelab.com/zh_CN/docs/NEWSMS/REST-API/API-SMS-Sending
type sendSMSRequest struct {
	To           []string               `json:"to"`       // 目标手机号列表，必填
	Template     sendSMSRequestTemplate `json:"template"` // 模板 id + 变量 params，必填
	PlanName     string                 `json:"plan_name,omitempty"`
	ScheduleTime *int64                 `json:"schedule_time,omitempty"`
}

type sendSMSRequestTemplate struct {
	ID     string            `json:"id"`
	Params map[string]string `json:"params"`
}

// sendSMSResponse 短信发送响应，与 EngageLab 返回一致。
type sendSMSResponse struct {
	PlanID        string `json:"plan_id,omitempty"`
	TotalCount    int    `json:"total_count,omitempty"`
	AcceptedCount int    `json:"accepted_count,omitempty"`
	MessageID     string `json:"message_id,omitempty"`
	Code          int    `json:"code,omitempty"`
	Message       string `json:"message,omitempty"`
}

// RegisterRoutes 注册 HTTP 路由。
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/send/email", s.handleSendEmail)
	mux.HandleFunc("/email/delivery", s.handleLastDelivery)
	mux.HandleFunc("/send/sms", s.handleSendSMS)
}

// handleSendEmail 处理发送邮件请求：插入一条 email_gateway_send 记录并立即发送。
func (s *Server) handleSendEmail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req sendEmailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if len(req.To) == 0 {
		http.Error(w, "to is required", http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	toStr := joinEmails(req.To)

	// 为整个发送流程设置超时，避免 DB 或邮件通道长时间无响应导致请求挂起
	const sendTimeout = 90 * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), sendTimeout)
	defer cancel()

	// API 侧去重：同一时刻（短时间窗口）同一邮箱 + 同 subject/content 只发送一封
	// 说明：
	// - 主要用于解决客户端重试/并发导致的重复插入与重复发送
	// - 仅对单收件人场景做严格等值匹配（to 字段为逗号拼接，多个收件人难以等值匹配）
	const dedupWindow = 15 // seconds
	if len(req.To) == 1 {
		var existID int64
		var existStatus int8
		now := time.Now().Unix()
		err := s.db.QueryRowContext(ctx,
			"SELECT id, status FROM email_gateway_send WHERE `to` = ? AND subject = ? AND content = ? AND create_time >= ? ORDER BY id DESC LIMIT 1",
			toStr, req.Subject, req.Content, now-int64(dedupWindow),
		).Scan(&existID, &existStatus)
		if err == nil && existID > 0 {
			switch existStatus {
			case 1:
				writeJSON(w, http.StatusOK, sendEmailResponse{ID: existID, Status: "sent"})
				return
			case 0, 3:
				// 已存在待发送/发送中记录，避免重复发送
				writeJSON(w, http.StatusAccepted, sendEmailResponse{ID: existID, Status: "sending"})
				return
			}
		}
	}

	// 插入一条待发送记录
	now := time.Now().Unix()
	res, err := s.db.ExecContext(
		ctx,
		"INSERT INTO email_gateway_send (`from`, `to`, bcc, cc, subject, content, attach_id, status, create_time, code, update_time) "+
			"VALUES (?, ?, '', '', ?, ?, 0, 0, ?, '', 0)",
		s.defaultFrom,
		toStr,
		req.Subject,
		req.Content,
		now,
	)
	if err != nil {
		slog.Error("插入 email_gateway_send 失败", slog.Any("err", err))
		http.Error(w, "insert failed", http.StatusInternalServerError)
		return
	}

	id, err := res.LastInsertId()
	if err != nil {
		slog.Error("获取 LastInsertId 失败", slog.Any("err", err))
		http.Error(w, "insert id failed", http.StatusInternalServerError)
		return
	}

	// 立即尝试发送这一条
	if err := s.sender.SendByID(ctx, id); err != nil {
		slog.Error("通过 API 发送邮件失败", slog.Int64("id", id), slog.Any("err", err))
		writeJSON(w, http.StatusInternalServerError, sendEmailResponse{
			ID:     id,
			Status: "failed",
			Error:  err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, sendEmailResponse{
		ID:     id,
		Status: "sent",
	})
}

// smsChannel 表示一个短信发送通道，按 used 排序后依次尝试，失败则尝试下一通道。
type smsChannel struct {
	name string
	used int
	send func(context.Context, *sendSMSRequest) (sendSMSResponse, string)
}

// handleSendSMS 按 used 优先级依次尝试各通道，第一个失败则用下一个，直到成功或全部失败；并写入 smsagent_send 表。
// used: -1 表示该配置跳过；>=0 表示使用中，数值大者优先尝试。接口请求/响应格式不变。
// POST /sms/send，请求体: { "to": ["+8618701235678"], "template": { "id": "xxx", "params": { "content": "..." } } }
func (s *Server) handleSendSMS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req sendSMSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if len(req.To) == 0 {
		http.Error(w, "to is required", http.StatusBadRequest)
		return
	}
	if req.Template.ID == "" && (req.Template.Params == nil || req.Template.Params["content"] == "") {
		http.Error(w, "template.id or template.params[content] is required", http.StatusBadRequest)
		return
	}

	channels := s.collectSMSChannels()
	if len(channels) == 0 {
		slog.Error("无可用短信通道", slog.String("hint", "sms.engagelab_sms / sms.mxt_sms 未配置或 used=-1"))
		http.Error(w, "sms not configured or all disabled (used=-1)", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	var apiResp sendSMSResponse
	var proxyName string

	for i, ch := range channels {
		resp, name := ch.send(ctx, &req)
		success := resp.Code == 0 && (resp.AcceptedCount > 0 || resp.PlanID != "")
		if success {
			apiResp = resp
			proxyName = name
			slog.Info("短信发送成功", slog.String("channel", ch.name), slog.String("plan_id", resp.PlanID))
			break
		}
		slog.Warn("短信通道发送失败，尝试下一通道",
			slog.String("channel", ch.name),
			slog.Int("code", resp.Code),
			slog.String("message", resp.Message),
			slog.Int("next_index", i+1))
		apiResp = resp
		proxyName = name
	}

	// 统一写入 smsagent_send
	responseStr := apiResp.PlanID
	if responseStr == "" {
		responseStr = apiResp.Message
	}
	if len(responseStr) > 1024 {
		responseStr = responseStr[:1024]
	}
	status := 0
	if apiResp.Code == 0 && (apiResp.AcceptedCount > 0 || apiResp.PlanID != "") {
		status = 1
	}
	for _, to := range req.To {
		t := to
		if len(t) > 24 {
			t = t[:24]
		}
		_, _ = s.db.ExecContext(ctx,
			"INSERT INTO smsagent_send (to_number, msg, msg_len, proxy_name, response, status, `type`, batch_id, code) VALUES (?, ?, 0, ?, ?, ?, 0, 0, '')",
			t, "", proxyName, responseStr, status)
	}

	slog.Info("短信发送请求完成",
		slog.Any("to", req.To),
		slog.String("template_id", req.Template.ID),
		slog.String("plan_id", apiResp.PlanID),
		slog.Int("accepted_count", apiResp.AcceptedCount),
		slog.Int("code", apiResp.Code))

	writeJSON(w, http.StatusOK, apiResp)
}

// collectSMSChannels 从 sms.engagelab_sms / sms.mxt_sms 收集可用通道，按 used 降序排列。
func (s *Server) collectSMSChannels() []smsChannel {
	var list []smsChannel
	eng := s.smsCfg.EngagelabSms
	if eng != nil && eng.Used >= 0 && eng.Endpoint != "" && eng.DevKey != "" && eng.DevSecret != "" {
		cfg := eng
		list = append(list, smsChannel{
			name: "engagelab_sms",
			used: cfg.Used,
			send: func(ctx context.Context, req *sendSMSRequest) (sendSMSResponse, string) {
				return s.sendSMSViaEngagelab(ctx, cfg, req)
			},
		})
	}
	mxt := s.smsCfg.MxtSms
	if mxt != nil && mxt.Used >= 0 && mxt.SubmitURL != "" && mxt.Account != "" && mxt.Pswd != "" {
		cfg := mxt
		list = append(list, smsChannel{
			name: "mxt_sms",
			used: cfg.Used,
			send: func(ctx context.Context, req *sendSMSRequest) (sendSMSResponse, string) {
				return s.sendSMSViaMxt(ctx, cfg, req)
			},
		})
	}
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].used > list[i].used {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	return list
}

// sendSMSViaEngagelab 使用 engagelab_sms 配置通过 EngageLab 发送，返回响应与 proxy_name。
func (s *Server) sendSMSViaEngagelab(ctx context.Context, cfg *config.EngagelabSmsConfig, req *sendSMSRequest) (sendSMSResponse, string) {
	proxyName := cfg.ProxyName
	if proxyName == "" {
		proxyName = "engagelab"
	}
	body := map[string]interface{}{
		"to":       req.To,
		"template": map[string]interface{}{"id": req.Template.ID, "params": req.Template.Params},
	}
	if req.PlanName != "" {
		body["plan_name"] = req.PlanName
	}
	if req.ScheduleTime != nil {
		body["schedule_time"] = *req.ScheduleTime
	}
	bodyBytes, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return sendSMSResponse{Code: -1, Message: err.Error()}, proxyName
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(cfg.DevKey+":"+cfg.DevSecret)))
	timeout := 15 * time.Second
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return sendSMSResponse{Code: -1, Message: err.Error()}, proxyName
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return sendSMSResponse{Code: -1, Message: err.Error()}, proxyName
	}
	var apiResp sendSMSResponse
	_ = json.Unmarshal(respBody, &apiResp)
	return apiResp, proxyName
}

// sendSMSViaMxt 使用 mxt_sms 配置通过梦网 MXT 单发/群发接口发送，请求体用 template.params["content"] 作为短信内容。
// 单发: https://www.mxtong.com/OtherViewkaf05.asp?ID=92  群发: https://www.mxtong.com/OtherViewkaf05.asp?ID=93
func (s *Server) sendSMSViaMxt(ctx context.Context, cfg *config.MxtSmsConfig, req *sendSMSRequest) (sendSMSResponse, string) {
	proxyName := cfg.ProxyName
	if proxyName == "" {
		proxyName = "mxt"
	}
	msg := req.Template.Params["content"]
	if msg == "" {
		for _, v := range req.Template.Params {
			msg = v
			break
		}
	}
	if msg == "" {
		return sendSMSResponse{Code: -1, Message: "template.params[content] required for mxt"}, proxyName
	}
	mobile := strings.Join(req.To, ",")
	params := url.Values{}
	params.Set("account", cfg.Account)
	params.Set("pswd", cfg.Pswd)
	params.Set("mobile", mobile)
	params.Set("msg", msg)
	if cfg.NeedStatus {
		params.Set("needstatus", "true")
	} else {
		params.Set("needstatus", "false")
	}
	if cfg.Product != "" {
		params.Set("product", cfg.Product)
	}
	params.Set("resptype", "json")

	submitURL := cfg.SubmitURL
	if len(req.To) > 1 {
		submitURL = cfg.BatchSubmitURL
		if submitURL == "" {
			submitURL = strings.ReplaceAll(cfg.SubmitURL, "HttpSendSM", "HttpBatchSendSM")
		}
	}

	reqURL := submitURL
	if !strings.Contains(submitURL, "?") {
		reqURL += "?"
	} else {
		reqURL += "&"
	}
	reqURL += params.Encode()

	timeout := 15 * time.Second
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return sendSMSResponse{Code: -1, Message: err.Error()}, proxyName
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return sendSMSResponse{Code: -1, Message: err.Error()}, proxyName
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return sendSMSResponse{Code: -1, Message: err.Error()}, proxyName
	}

	// 梦网返回 resptype=json 时格式可能为两行或 json，先尝试按两行解析
	bodyStr := string(respBody)
	lines := strings.SplitN(strings.TrimSpace(bodyStr), "\n", 2)
	var respStatus string
	var msgID string
	if len(lines) >= 1 {
		parts := strings.SplitN(lines[0], ",", 2)
		if len(parts) >= 2 {
			respStatus = strings.TrimSpace(parts[1])
		}
	}
	if len(lines) >= 2 && strings.TrimSpace(lines[1]) != "" {
		msgID = strings.TrimSpace(lines[1])
	}
	code := 0
	if respStatus != "0" {
		code = 1
		if respStatus != "" {
			if n, err := fmt.Sscanf(respStatus, "%d", &code); n != 1 || err != nil {
				code = 1
			}
		}
	}
	return sendSMSResponse{
		PlanID:        msgID,
		TotalCount:    len(req.To),
		AcceptedCount: len(req.To),
		MessageID:     msgID,
		Code:          code,
		Message:       respStatus,
	}, proxyName
}

// handleLastDelivery 根据 EngageLab 投递回应接口查询收件人投递状态。
// GET /email/delivery?to=user@example.com&send_date=2025-03-11（send_date 可选，默认当天 yyyy-MM-dd）
// 文档: https://www.engagelab.com/zh_CN/docs/email/rest-api/delivery-response
func (s *Server) handleLastDelivery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	emailAddr := r.URL.Query().Get("to")
	if emailAddr == "" {
		http.Error(w, "缺少参数 to（收件人地址）", http.StatusBadRequest)
		return
	}

	sendDate := r.URL.Query().Get("send_date")
	if sendDate == "" {
		sendDate = time.Now().Format("2006-01-02")
	}

	// 由发送接口地址推导投递状态接口地址，如 https://email.api.engagelab.cc/v1/mail/send -> .../v1/email_status
	u, err := url.Parse(s.restCfg.Endpoint)
	if err != nil {
		slog.Error("解析 REST endpoint 失败", slog.String("endpoint", s.restCfg.Endpoint), slog.Any("err", err))
		http.Error(w, "bad config", http.StatusInternalServerError)
		return
	}
	u.Path = "/v1/email_status"
	u.RawQuery = ""
	emailStatusURL := u.String()

	reqURL := fmt.Sprintf("%s?send_date=%s&email=%s&limit=1&offset=0", emailStatusURL, url.QueryEscape(sendDate), url.QueryEscape(emailAddr))

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, reqURL, nil)
	if err != nil {
		slog.Error("创建投递状态请求失败", slog.String("to", emailAddr), slog.Any("err", err))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if s.restCfg.APIUser != "" && s.restCfg.APIKey != "" {
		token := base64.StdEncoding.EncodeToString([]byte(s.restCfg.APIUser + ":" + s.restCfg.APIKey))
		req.Header.Set("Authorization", "Basic "+token)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("调用投递状态接口失败", slog.String("to", emailAddr), slog.Any("err", err))
		http.Error(w, "call email_status failed", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("读取投递状态响应失败", slog.String("to", emailAddr), slog.Any("err", err))
		http.Error(w, "read response failed", http.StatusInternalServerError)
		return
	}

	if resp.StatusCode != http.StatusOK {
		slog.Error("投递状态接口返回非 200", slog.Int("status", resp.StatusCode), slog.String("body", string(body)))
		writeJSON(w, resp.StatusCode, map[string]string{"error": string(body)})
		return
	}

	var result engagelabEmailStatusResult
	if err := json.Unmarshal(body, &result); err != nil {
		slog.Error("解析投递状态响应失败", slog.String("to", emailAddr), slog.Any("err", err), slog.String("body", string(body)))
		http.Error(w, "parse response failed", http.StatusInternalServerError)
		return
	}

	if len(result.Result) == 0 {
		slog.Info("查询投递记录未找到", slog.String("to", emailAddr), slog.String("send_date", sendDate), slog.Bool("found", false))
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"found":     false,
			"msg":       "未找到该收件人地址的投递记录",
			"send_date": sendDate,
		})
		return
	}

	item := result.Result[0]

	// 日志中保留完整的投递回应信息，便于排查问题。
	slog.Info("查询投递记录",
		slog.String("email", item.Email),
		slog.String("email_id", item.EmailID),
		slog.String("api_user", item.APIUser),
		slog.Int("status", item.Status),
		slog.String("status_desc", item.StatusDesc),
		slog.Int("sub_status", item.SubStatus),
		slog.String("sub_status_desc", item.SubStatusDesc),
		slog.String("request_time", item.RequestTime),
		slog.String("update_time", item.UpdateTime),
		slog.String("response_message", item.ResponseMessage))

	// 对外返回精简后的结构：只包含 api_user、email、status 及说明。
	writeJSON(w, http.StatusOK, deliverySimpleResponse{
		APIUser:    item.APIUser,
		Email:      item.Email,
		Status:     item.Status,
		StatusDesc: item.StatusDesc,
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func joinEmails(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return fmt.Sprintf("%s", list[0]) + joinRest(list[1:])
}

func joinRest(list []string) string {
	if len(list) == 0 {
		return ""
	}
	s := ""
	for _, e := range list {
		s += "," + e
	}
	return s
}
