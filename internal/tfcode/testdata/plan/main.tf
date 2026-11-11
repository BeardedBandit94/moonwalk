variable "secret" {
  sensitive = true
  default   = "hunter2"
}
resource "terraform_data" "keep" {
  input = "same"
}
resource "terraform_data" "update" {
  input = "v2"
}
resource "terraform_data" "replace" {
  triggers_replace = "b"
}
resource "terraform_data" "create" {
  input = {
    from   = terraform_data.keep.output
    subnet = module.net.first
    secret = var.secret
  }
  depends_on = [terraform_data.update]
}
module "net" {
  source = "./m"
  base   = terraform_data.keep.output
}
