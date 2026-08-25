package config

type WhatsAppSettings struct {
	BridgeURL        string `json:"bridge_url"         yaml:"-" env:"MINTCLAW_CHANNELS_WHATSAPP_BRIDGE_URL"`
	UseNative        bool   `json:"use_native"         yaml:"-" env:"MINTCLAW_CHANNELS_WHATSAPP_USE_NATIVE"`
	SessionStorePath string `json:"session_store_path" yaml:"-" env:"MINTCLAW_CHANNELS_WHATSAPP_SESSION_STORE_PATH"`
}

type TelegramSettings struct {
	Token             SecureString       `json:"token,omitzero"              yaml:"token,omitempty" env:"MINTCLAW_CHANNELS_TELEGRAM_TOKEN"`
	BaseURL           string             `json:"base_url"                    yaml:"-"               env:"MINTCLAW_CHANNELS_TELEGRAM_BASE_URL"`
	Proxy             string             `json:"proxy"                       yaml:"-"               env:"MINTCLAW_CHANNELS_TELEGRAM_PROXY"`
	Streaming         StreamingConfig    `json:"streaming,omitzero"          yaml:"-"`
	RichMessages      RichMessagesConfig `json:"rich_messages,omitzero"      yaml:"-"`
	UseMarkdownV2     bool               `json:"use_markdown_v2"             yaml:"-"               env:"MINTCLAW_CHANNELS_TELEGRAM_USE_MARKDOWN_V2"`
	MediaGroupDelayMS int                `json:"media_group_delay_ms"        yaml:"-"               env:"MINTCLAW_CHANNELS_TELEGRAM_MEDIA_GROUP_DELAY_MS"`
	AllowedTopicIDs   []string           `json:"allowed_topic_ids,omitempty" yaml:"-"               env:"MINTCLAW_CHANNELS_TELEGRAM_ALLOWED_TOPIC_IDS"`
	IgnoredTopicIDs   []string           `json:"ignored_topic_ids,omitempty" yaml:"-"               env:"MINTCLAW_CHANNELS_TELEGRAM_IGNORED_TOPIC_IDS"`
}

type RichMessagesConfig struct {
	Enabled bool `json:"enabled" yaml:"-" env:"MINTCLAW_CHANNELS_TELEGRAM_RICH_MESSAGES_ENABLED"`
}

func (c RichMessagesConfig) IsZero() bool {
	return !c.Enabled
}

type FeishuSettings struct {
	AppID               string       `json:"app_id"                      yaml:"-"                            env:"MINTCLAW_CHANNELS_FEISHU_APP_ID"`
	AppSecret           SecureString `json:"app_secret,omitzero"         yaml:"app_secret,omitempty"         env:"MINTCLAW_CHANNELS_FEISHU_APP_SECRET"`
	EncryptKey          SecureString `json:"encrypt_key,omitzero"        yaml:"encrypt_key,omitempty"        env:"MINTCLAW_CHANNELS_FEISHU_ENCRYPT_KEY"`
	VerificationToken   SecureString `json:"verification_token,omitzero" yaml:"verification_token,omitempty" env:"MINTCLAW_CHANNELS_FEISHU_VERIFICATION_TOKEN"`
	RandomReactionEmoji []string     `json:"random_reaction_emoji"       yaml:"-"                            env:"MINTCLAW_CHANNELS_FEISHU_RANDOM_REACTION_EMOJI"`
	IsLark              bool         `json:"is_lark"                     yaml:"-"                            env:"MINTCLAW_CHANNELS_FEISHU_IS_LARK"`
}

type DiscordSettings struct {
	Token       SecureString `json:"token,omitzero" yaml:"token,omitempty" env:"MINTCLAW_CHANNELS_DISCORD_TOKEN"`
	Proxy       string       `json:"proxy"          yaml:"-"               env:"MINTCLAW_CHANNELS_DISCORD_PROXY"`
	MentionOnly bool         `json:"mention_only"   yaml:"-"               env:"MINTCLAW_CHANNELS_DISCORD_MENTION_ONLY"`
}

type MaixCamSettings struct {
	Host string `json:"host" yaml:"-" env:"MINTCLAW_CHANNELS_MAIXCAM_HOST"`
	Port int    `json:"port" yaml:"-" env:"MINTCLAW_CHANNELS_MAIXCAM_PORT"`
}

type QQSettings struct {
	AppID                string       `json:"app_id"                   yaml:"-"                    env:"MINTCLAW_CHANNELS_QQ_APP_ID"`
	AppSecret            SecureString `json:"app_secret,omitzero"      yaml:"app_secret,omitempty" env:"MINTCLAW_CHANNELS_QQ_APP_SECRET"`
	MaxMessageLength     int          `json:"max_message_length"       yaml:"-"                    env:"MINTCLAW_CHANNELS_QQ_MAX_MESSAGE_LENGTH"`
	MaxBase64FileSizeMiB int64        `json:"max_base64_file_size_mib" yaml:"-"                    env:"MINTCLAW_CHANNELS_QQ_MAX_BASE64_FILE_SIZE_MIB"`
	SendMarkdown         bool         `json:"send_markdown"            yaml:"-"                    env:"MINTCLAW_CHANNELS_QQ_SEND_MARKDOWN"`
}

type DingTalkSettings struct {
	ClientID     string       `json:"client_id"              yaml:"-"                       env:"MINTCLAW_CHANNELS_DINGTALK_CLIENT_ID"`
	ClientSecret SecureString `json:"client_secret,omitzero" yaml:"client_secret,omitempty" env:"MINTCLAW_CHANNELS_DINGTALK_CLIENT_SECRET"`
}

type SlackSettings struct {
	BotToken          SecureString `json:"bot_token,omitzero"            yaml:"bot_token,omitempty" env:"MINTCLAW_CHANNELS_SLACK_BOT_TOKEN"`
	AppToken          SecureString `json:"app_token,omitzero"            yaml:"app_token,omitempty" env:"MINTCLAW_CHANNELS_SLACK_APP_TOKEN"`
	AllowedChannelIDs []string     `json:"allowed_channel_ids,omitempty" yaml:"-"                   env:"MINTCLAW_CHANNELS_SLACK_ALLOWED_CHANNEL_IDS"`
	IgnoredChannelIDs []string     `json:"ignored_channel_ids,omitempty" yaml:"-"                   env:"MINTCLAW_CHANNELS_SLACK_IGNORED_CHANNEL_IDS"`
}

type MatrixSettings struct {
	Homeserver         string       `json:"homeserver"                     yaml:"-"                      env:"MINTCLAW_CHANNELS_MATRIX_HOMESERVER"`
	UserID             string       `json:"user_id"                        yaml:"-"                      env:"MINTCLAW_CHANNELS_MATRIX_USER_ID"`
	AccessToken        SecureString `json:"access_token,omitzero"          yaml:"access_token,omitempty" env:"MINTCLAW_CHANNELS_MATRIX_ACCESS_TOKEN"`
	DeviceID           string       `json:"device_id,omitempty"            yaml:"-"`
	JoinOnInvite       bool         `json:"join_on_invite"                 yaml:"-"`
	MessageFormat      string       `json:"message_format,omitempty"       yaml:"-"`
	CryptoDatabasePath string       `json:"crypto_database_path,omitempty" yaml:"-"`
	CryptoPassphrase   string       `json:"crypto_passphrase,omitempty"    yaml:"-"`
}

// DeltaChatSettings configures the Delta Chat channel. Delta Chat is an
// email-based, end-to-end encrypted messenger; MintClaw talks to a local
// `deltachat-rpc-server` process over JSON-RPC (stdio).
//
// Email is the only required setting. A full address selects an already
// configured account in DataDir; a first-run marker such as "@nine.testrun.org"
// creates a chatmail account and tells the user which full email to save.
// Mailbox credentials stay in the Delta Chat account store. DisplayName and
// AvatarImage are optional profile settings applied on startup.
type DeltaChatSettings struct {
	Email          string `json:"email"                     yaml:"-" env:"MINTCLAW_CHANNELS_DELTACHAT_EMAIL"`
	DisplayName    string `json:"display_name,omitempty"    yaml:"-" env:"MINTCLAW_CHANNELS_DELTACHAT_DISPLAY_NAME"`
	AvatarImage    string `json:"avatar_image,omitempty"    yaml:"-" env:"MINTCLAW_CHANNELS_DELTACHAT_AVATAR_IMAGE"`
	DataDir        string `json:"data_dir,omitempty"        yaml:"-" env:"MINTCLAW_CHANNELS_DELTACHAT_DATA_DIR"`
	RPCServerPath  string `json:"rpc_server_path,omitempty" yaml:"-" env:"MINTCLAW_CHANNELS_DELTACHAT_RPC_SERVER_PATH"`
	InviteLink     string `json:"invite_link,omitempty"     yaml:"-" env:"MINTCLAW_CHANNELS_DELTACHAT_INVITE_LINK"`
	AllowCrosspost bool   `json:"allow_crosspost,omitempty" yaml:"-" env:"MINTCLAW_CHANNELS_DELTACHAT_ALLOW_CROSSPOST"`
}

type LINESettings struct {
	ChannelSecret      SecureString `json:"channel_secret,omitzero"       yaml:"channel_secret,omitempty"       env:"MINTCLAW_CHANNELS_LINE_CHANNEL_SECRET"`
	ChannelAccessToken SecureString `json:"channel_access_token,omitzero" yaml:"channel_access_token,omitempty" env:"MINTCLAW_CHANNELS_LINE_CHANNEL_ACCESS_TOKEN"`
	WebhookHost        string       `json:"webhook_host"                  yaml:"-"                              env:"MINTCLAW_CHANNELS_LINE_WEBHOOK_HOST"`
	WebhookPort        int          `json:"webhook_port"                  yaml:"-"                              env:"MINTCLAW_CHANNELS_LINE_WEBHOOK_PORT"`
	WebhookPath        string       `json:"webhook_path"                  yaml:"-"                              env:"MINTCLAW_CHANNELS_LINE_WEBHOOK_PATH"`
}

type OneBotSettings struct {
	WSUrl              string       `json:"ws_url"                yaml:"-"                      env:"MINTCLAW_CHANNELS_ONEBOT_WS_URL"`
	AccessToken        SecureString `json:"access_token,omitzero" yaml:"access_token,omitempty" env:"MINTCLAW_CHANNELS_ONEBOT_ACCESS_TOKEN"`
	ReconnectInterval  int          `json:"reconnect_interval"    yaml:"-"                      env:"MINTCLAW_CHANNELS_ONEBOT_RECONNECT_INTERVAL"`
	GroupTriggerPrefix []string     `json:"group_trigger_prefix"  yaml:"-"                      env:"MINTCLAW_CHANNELS_ONEBOT_GROUP_TRIGGER_PREFIX"`
}

type WeComGroupConfig struct {
	AllowFrom []string `json:"allow_from,omitempty"`
}

type WeComSettings struct {
	BotID               string          `json:"bot_id"                  yaml:"-"                env:"BOT_ID"`
	Secret              SecureString    `json:"secret,omitzero"         yaml:"secret,omitempty" env:"SECRET"`
	WebSocketURL        string          `json:"websocket_url,omitempty" yaml:"-"                env:"WEBSOCKET_URL"`
	SendThinkingMessage bool            `json:"send_thinking_message"   yaml:"-"                env:"SEND_THINKING_MESSAGE"`
	Streaming           StreamingConfig `json:"streaming,omitzero"      yaml:"-"`
}

func (c *WeComSettings) SetSecret(secret string) {
	c.Secret = *NewSecureString(secret)
}

type WeixinSettings struct {
	Token      SecureString `json:"token,omitzero"       yaml:"token,omitempty" env:"MINTCLAW_CHANNELS_WEIXIN_TOKEN"`
	AccountID  string       `json:"account_id,omitempty" yaml:"-"               env:"MINTCLAW_CHANNELS_WEIXIN_ACCOUNT_ID"`
	BaseURL    string       `json:"base_url"             yaml:"-"               env:"MINTCLAW_CHANNELS_WEIXIN_BASE_URL"`
	CDNBaseURL string       `json:"cdn_base_url"         yaml:"-"               env:"MINTCLAW_CHANNELS_WEIXIN_CDN_BASE_URL"`
	Proxy      string       `json:"proxy"                yaml:"-"               env:"MINTCLAW_CHANNELS_WEIXIN_PROXY"`
}

// SetToken sets the Weixin token and marks it as dirty for security saving
func (c *WeixinSettings) SetToken(token string) {
	c.Token = *NewSecureString(token)
}

type MintClawSettings struct {
	Token           SecureString    `json:"token,omitzero"              yaml:"token,omitempty" env:"MINTCLAW_CHANNELS_MINTCLAW_TOKEN"`
	AllowTokenQuery bool            `json:"allow_token_query,omitempty" yaml:"-"`
	AllowOrigins    []string        `json:"allow_origins,omitempty"     yaml:"-"`
	Streaming       StreamingConfig `json:"streaming,omitzero"          yaml:"-"`
	PingInterval    int             `json:"ping_interval,omitempty"     yaml:"-"`
	ReadTimeout     int             `json:"read_timeout,omitempty"      yaml:"-"`
	WriteTimeout    int             `json:"write_timeout,omitempty"     yaml:"-"`
	MaxConnections  int             `json:"max_connections,omitempty"   yaml:"-"`
}

// SetToken sets the MintClaw token and marks it as dirty for security saving
func (c *MintClawSettings) SetToken(token string) {
	c.Token = *NewSecureString(token)
}

type MintClawClientSettings struct {
	URL          string       `json:"url"                     yaml:"-"               env:"MINTCLAW_CHANNELS_MINTCLAW_CLIENT_URL"`
	Token        SecureString `json:"token,omitzero"          yaml:"token,omitempty" env:"MINTCLAW_CHANNELS_MINTCLAW_CLIENT_TOKEN"`
	SessionID    string       `json:"session_id,omitempty"    yaml:"-"`
	PingInterval int          `json:"ping_interval,omitempty" yaml:"-"`
	ReadTimeout  int          `json:"read_timeout,omitempty"  yaml:"-"`
}

type IRCSettings struct {
	Server           string       `json:"server"                     yaml:"-"                           env:"MINTCLAW_CHANNELS_IRC_SERVER"`
	TLS              bool         `json:"tls"                        yaml:"-"                           env:"MINTCLAW_CHANNELS_IRC_TLS"`
	Nick             string       `json:"nick"                       yaml:"-"                           env:"MINTCLAW_CHANNELS_IRC_NICK"`
	User             string       `json:"user,omitempty"             yaml:"-"                           env:"MINTCLAW_CHANNELS_IRC_USER"`
	RealName         string       `json:"real_name,omitempty"        yaml:"-"`
	Password         SecureString `json:"password,omitzero"          yaml:"password,omitempty"          env:"MINTCLAW_CHANNELS_IRC_PASSWORD"`
	NickServPassword SecureString `json:"nickserv_password,omitzero" yaml:"nickserv_password,omitempty" env:"MINTCLAW_CHANNELS_IRC_NICKSERV_PASSWORD"`
	SASLUser         string       `json:"sasl_user"                  yaml:"-"                           env:"MINTCLAW_CHANNELS_IRC_SASL_USER"`
	SASLPassword     SecureString `json:"sasl_password,omitzero"     yaml:"sasl_password,omitempty"     env:"MINTCLAW_CHANNELS_IRC_SASL_PASSWORD"`
	Channels         []string     `json:"channels"                   yaml:"-"                           env:"MINTCLAW_CHANNELS_IRC_CHANNELS"`
	RequestCaps      []string     `json:"request_caps,omitempty"     yaml:"-"`
}

type VKSettings struct {
	Token   SecureString `json:"token,omitzero" yaml:"token,omitempty" env:"MINTCLAW_CHANNELS_VK_TOKEN"`
	GroupID int          `json:"group_id"       yaml:"-"               env:"MINTCLAW_CHANNELS_VK_GROUP_ID"`
}

func (c *VKSettings) SetToken(token string) {
	c.Token = *NewSecureString(token)
}

// TeamsWebhookSettings configures the output-only Microsoft Teams webhook channel.
// Multiple webhook targets can be configured and selected via ChatID at send time.
type TeamsWebhookSettings struct {
	Webhooks map[string]TeamsWebhookTarget `json:"webhooks" yaml:"webhooks,omitempty"`
}

// TeamsWebhookTarget represents a single Teams webhook destination.
type TeamsWebhookTarget struct {
	WebhookURL SecureString `json:"webhook_url,omitzero" yaml:"webhook_url,omitempty"`
	Title      string       `json:"title,omitempty"      yaml:"-"`
}

type MQTTSettings struct {
	Broker      string       `json:"broker"                 yaml:"-"                  env:"MINTCLAW_CHANNELS_MQTT_BROKER"`
	AgentID     string       `json:"agent_id"               yaml:"-"                  env:"MINTCLAW_CHANNELS_MQTT_AGENT_ID"`
	TopicPrefix string       `json:"topic_prefix,omitempty" yaml:"-"                  env:"MINTCLAW_CHANNELS_MQTT_TOPIC_PREFIX"`
	Username    SecureString `json:"username,omitzero"      yaml:"username,omitempty" env:"MINTCLAW_CHANNELS_MQTT_USERNAME"`
	Password    SecureString `json:"password,omitzero"      yaml:"password,omitempty" env:"MINTCLAW_CHANNELS_MQTT_PASSWORD"`
	ClientID    string       `json:"client_id,omitempty"    yaml:"-"                  env:"MINTCLAW_CHANNELS_MQTT_CLIENT_ID"`
	KeepAlive   int          `json:"keep_alive,omitempty"   yaml:"-"                  env:"MINTCLAW_CHANNELS_MQTT_KEEP_ALIVE"`
	QoS         int          `json:"qos,omitempty"          yaml:"-"                  env:"MINTCLAW_CHANNELS_MQTT_QOS"`
}

// SlackWebhookSettings configures the output-only Slack webhook channel.
type SlackWebhookSettings struct {
	Webhooks map[string]SlackWebhookTarget `json:"webhooks" yaml:"webhooks,omitempty"`
}

// SlackWebhookTarget represents a single Slack Incoming Webhook destination.
type SlackWebhookTarget struct {
	WebhookURL SecureString `json:"webhook_url,omitzero" yaml:"webhook_url,omitempty"`
	Username   string       `json:"username,omitempty"   yaml:"-"`
	IconEmoji  string       `json:"icon_emoji,omitempty" yaml:"-"`
}
