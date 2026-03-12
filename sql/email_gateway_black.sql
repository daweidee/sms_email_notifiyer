CREATE TABLE `email_gateway_black` (
  `id` int unsigned NOT NULL AUTO_INCREMENT,
  `email` varchar(50) NOT NULL DEFAULT '' COMMENT '邮箱名称',
  `status` tinyint(1) NOT NULL DEFAULT '0' COMMENT '0：有效，1：无效',
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb3;