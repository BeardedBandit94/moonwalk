# A .tofu file, which only OpenTofu reads.

resource "aws_iam_role" "app" {
  name               = "app"
  assume_role_policy = "{}"
}

resource "aws_iam_policy" "read" {
  name   = "read"
  policy = "{}"
}

resource "aws_iam_role_policy_attachment" "app" {
  role       = aws_iam_role.app.name
  policy_arn = aws_iam_policy.read.arn
}
