variable "base" {}
resource "terraform_data" "subnet" {
  count = 3
  input = var.base
}
resource "terraform_data" "route" {
  input = terraform_data.subnet[0].id
}
output "first" {
  value = terraform_data.subnet[0].id
}
