CREATE TABLE `smsagent_send` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT COMMENT '自增，唯一',
  `to_number` char(24) NOT NULL DEFAULT '' COMMENT '用户手机号',
  `msg` varchar(2048) NOT NULL DEFAULT '' COMMENT '发送消息',
  `msg_len` int(11) NOT NULL DEFAULT '0' COMMENT '发送信息长度',
  `proxy_name` char(20) NOT NULL DEFAULT '' COMMENT '渠道标识',
  `response` varchar(1024) NOT NULL DEFAULT '' COMMENT '渠道返回的信息（渠道返回错误码或者流水号）',
  `status` tinyint(1) NOT NULL COMMENT '渠道接受状态，0，接受失败，1，接受成功',
  `type` int(11) NOT NULL DEFAULT '0' COMMENT '业务类型（注册，找回密码，充值等），需要制定业务码',
  `batch_id` bigint(20) NOT NULL DEFAULT '0' COMMENT '批ID，一次请求1000号码，代表一个批处理',
  `create_time` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间戳',
  `code` varchar(50) NOT NULL DEFAULT '' COMMENT '加密短信验证码',
  PRIMARY KEY (`id`),
  KEY `idx_create_time` (`create_time`),
  KEY `idx_to_number` (`to_number`)
) ENGINE=InnoDB AUTO_INCREMENT=1 DEFAULT CHARSET=utf8;