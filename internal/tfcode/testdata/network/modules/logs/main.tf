variable "vpc_id" {}
variable "name" {}

resource "aws_cloudwatch_log_group" "flow" {
  name = "/flow/${var.name}"
}

resource "aws_flow_log" "vpc" {
  vpc_id          = var.vpc_id
  log_destination = aws_cloudwatch_log_group.flow.arn
  traffic_type    = "ALL"
}

output "group_name" {
  value = aws_cloudwatch_log_group.flow.name
}
