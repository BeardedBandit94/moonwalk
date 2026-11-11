# Matches internal/tfstate/testdata/network.tfstate, plus resources that
# have not been applied yet.

locals {
  network_id = aws_vpc.main.id
  name       = "app"
}

resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_subnet" "private" {
  count      = 2
  vpc_id     = aws_vpc.main.id
  cidr_block = cidrsubnet(aws_vpc.main.cidr_block, 8, count.index)
}

resource "aws_route_table" "private" {
  vpc_id = local.network_id
}

resource "aws_route_table_association" "private" {
  count          = 2
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private.id
}

resource "aws_instance" "app" {
  ami                  = data.aws_ami.linux.id
  instance_type        = "t3.micro"
  subnet_id            = aws_subnet.private[1].id
  iam_instance_profile = local.name

  vpc_security_group_ids = [aws_security_group.app.id]
}

resource "aws_lb" "web" {
  internal = false
  subnets  = aws_subnet.private[*].id
}

# Declared here but not in the state yet.
resource "aws_security_group" "app" {
  name   = local.name
  vpc_id = local.network_id

  dynamic "ingress" {
    for_each = [443]
    content {
      from_port       = ingress.value
      to_port         = ingress.value
      protocol        = "tcp"
      security_groups = [aws_security_group.lb.id]
    }
  }
}

resource "aws_security_group" "lb" {
  name   = "${local.name}-lb"
  vpc_id = aws_vpc.main.id
}

data "aws_ami" "linux" {
  most_recent = true
  owners      = ["amazon"]
}

module "logs" {
  source = "./modules/logs"
  vpc_id = aws_vpc.main.id
  name   = local.name
}

resource "aws_cloudwatch_log_metric_filter" "errors" {
  name           = "errors"
  pattern        = "ERROR"
  log_group_name = module.logs.group_name

  depends_on = [aws_lb.web]
}
