CREATE TABLE `email_gateway_attach` (
  `id` int unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `content` longtext NOT NULL COMMENT '附件内容，base64加密',
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb3;