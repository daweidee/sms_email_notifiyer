package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 是应用的整体配置。
type Config struct {
	Log    LogConfig    `yaml:"log"`
	Server ServerConfig `yaml:"server"`
	DB     DBConfig     `yaml:"db"`
	Email  EmailConfig  `yaml:"email"`
	SMS    SMSConfig    `yaml:"sms"` // 短信多通道：engagelab_sms / mxt_sms，used 越大越优先，失败则下一通道
}

// SMSConfig 短信发送配置，支持多通道，格式与 config.yaml 中 sms 段一致。
type SMSConfig struct {
	EngagelabSms *EngagelabSmsConfig `yaml:"engagelab_sms"` // used: -1 不使用；正整数为优先级
	MxtSms       *MxtSmsConfig       `yaml:"mxt_sms"`
}

// EngagelabSmsConfig EngageLab 短信通道。used: -1 表示不使用；1、2… 表示优先级，数值越大越优先。
// 文档: https://www.engagelab.com/zh_CN/docs/NEWSMS/REST-API/API-SMS-Sending
type EngagelabSmsConfig struct {
	Used           int    `yaml:"used"`
	Endpoint       string `yaml:"endpoint"`
	DevKey         string `yaml:"dev_key"`
	DevSecret      string `yaml:"dev_secret"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	ProxyName      string `yaml:"proxy_name"`
}

// MxtSmsConfig 梦网 MXT 短信通道。used: -1 表示不使用；1、2… 表示优先级。
// 单发: https://www.mxtong.com/OtherViewkaf05.asp?ID=92  群发: https://www.mxtong.com/OtherViewkaf05.asp?ID=93
type MxtSmsConfig struct {
	Used           int    `yaml:"used"`
	SubmitURL      string `yaml:"submit_url"`
	BatchSubmitURL string `yaml:"batch_submit_url"`
	Account        string `yaml:"account"`
	Pswd           string `yaml:"pswd"`
	NeedStatus     bool   `yaml:"needstatus"`
	Product        string `yaml:"product"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	ProxyName      string `yaml:"proxy_name"`
}

// LogConfig 日志配置。
type LogConfig struct {
	Level string `yaml:"level"` // debug, info, warn, error
}

// ServerConfig HTTP 服务配置。
type ServerConfig struct {
	Host string `yaml:"host"` // 绑定 IP，例如 "0.0.0.0" 或 "127.0.0.1"
	Port int    `yaml:"port"` // 监听端口，例如 8080
}

// DBConfig 数据库配置。
type DBConfig struct {
	DSN                   string `yaml:"dsn"`                     // 例如: user:password@tcp(host:port)/dbname?timeout=10s&loc=Local&parseTime=true&allowOldPasswords=1
	ConnectTimeoutSeconds int    `yaml:"connect_timeout_seconds"` // 连接和连通性检查超时时间（秒），用于 Ping
	MaxConn               int    `yaml:"max_conn"`               // 最大打开连接数
	MaxIdle               int    `yaml:"max_idle"`               // 最大空闲连接数
}

// EmailConfig 邮件发送相关配置。支持多通道，按 used 排序后依次尝试，失败则下一通道。
type EmailConfig struct {
	EngagelabSmtp *EngagelabSmtpConfig `yaml:"engagelab_smtp"` // used: -1 不使用；正整数优先级，数值越大越优先
	EngagelabRest *EngagelabRestConfig `yaml:"engagelab_rest"`
	GodaddySmtp   *EngagelabSmtpConfig `yaml:"godaddy_smtp"`   // GoDaddy SMTP，结构与 engagelab_smtp 相同
}

// EngagelabSmtpConfig EngageLab SMTP 通道。used: -1 表示不使用；1、2… 表示优先级，数值越大越优先。
type EngagelabSmtpConfig struct {
	Used     int    `yaml:"used"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	From     string `yaml:"from"`     // 发件人邮箱地址
	FromName string `yaml:"from_name"` // 发件人显示名称，收件端显示为「名称」而非邮箱；为空则仅显示邮箱
}

// EngagelabRestConfig EngageLab REST 通道。used: -1 表示不使用；1、2… 表示优先级。
type EngagelabRestConfig struct {
	Used           int    `yaml:"used"`
	Endpoint       string `yaml:"endpoint"`
	APIKey         string `yaml:"api_key"`
	APIUser        string `yaml:"api_user"`
	From           string `yaml:"from"`     // 发件人邮箱地址
	FromName       string `yaml:"from_name"` // 发件人显示名称；为空则仅显示邮箱
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

// RestConfig 供 API 投递查询等使用的 REST 配置，可从 EngagelabRestConfig 转换。
type RestConfig struct {
	Endpoint       string
	APIKey         string
	APIUser        string
	From           string
	TimeoutSeconds int
}

// DefaultRestConfig 返回用于 API（如投递查询）的 REST 配置，优先取 engagelab_rest（used>=0）。
func (e *EmailConfig) DefaultRestConfig() RestConfig {
	if e == nil {
		return RestConfig{}
	}
	if r := e.EngagelabRest; r != nil && r.Used >= 0 {
		return RestConfig{
			Endpoint:       r.Endpoint,
			APIKey:         r.APIKey,
			APIUser:        r.APIUser,
			From:           r.From,
			TimeoutSeconds: r.TimeoutSeconds,
		}
	}
	return RestConfig{}
}

// DefaultFrom 返回 HTTP 发件接口使用的默认发件人，仅从 used >= 0 的通道中选取（used: -1 的通道不参与）。
func (e *EmailConfig) DefaultFrom() string {
	if e == nil {
		return ""
	}
	if r := e.EngagelabRest; r != nil && r.Used >= 0 && r.From != "" {
		return r.From
	}
	if s := e.EngagelabSmtp; s != nil && s.Used >= 0 && s.From != "" {
		return s.From
	}
	if g := e.GodaddySmtp; g != nil && g.Used >= 0 && g.From != "" {
		return g.From
	}
	return ""
}

// Load 从指定路径加载 YAML 配置。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	return &cfg, nil
}

