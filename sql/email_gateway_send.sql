CREATE TABLE `email_gateway_send` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `from` varchar(50) NOT NULL DEFAULT '' COMMENT '发送人的邮箱',
  `to` text NOT NULL COMMENT '收件人地址',
  `bcc` text NOT NULL COMMENT '暗抄送地址',
  `cc` text NOT NULL COMMENT '抄送地址',
  `subject` text NOT NULL COMMENT '主题内容',
  `content` text NOT NULL COMMENT '发送内容（text or HTML）',
  `attach_id` int unsigned NOT NULL DEFAULT '0' COMMENT '附件ID',
  `status` tinyint NOT NULL DEFAULT '0' COMMENT '0=未发送，1=已发送，2=发送失败，3=发送中(占用锁)',
  `create_time` int NOT NULL DEFAULT '0' COMMENT '发送时间戳',
  `code` varchar(50) NOT NULL DEFAULT '' COMMENT '加密邮件验证码',
  `update_time` int NOT NULL DEFAULT '0' COMMENT '修改时间戳',
  PRIMARY KEY (`id`),
  KEY `idx_create_time` (`create_time`)
) ENGINE=InnoDB AUTO_INCREMENT=1 DEFAULT CHARSET=utf8mb3;