package tfstate

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// syntheticState builds a raw state of modules×100 resources. Each module
// instance holds a network, 9 subnets and 90 machines; every machine depends
// on the network and on the subnet block, as state would record it.
func syntheticState(modules int) []byte {
	const provider = `"provider":"provider[\"registry.terraform.io/hashicorp/aws\"]"`
	var b bytes.Buffer
	b.WriteString(`{"version":4,"terraform_version":"1.9.5","resources":[`)
	for m := 0; m < modules; m++ {
		if m > 0 {
			b.WriteByte(',')
		}
		mod := fmt.Sprintf(`"module":"module.app[%d]"`, m)
		arn := func(kind string, i int) string {
			return fmt.Sprintf(`arn:aws:ec2:us-east-%d:%012d:%s/%s-%d-%d`, m%2+1, 100000000000+m%20, kind, kind, m, i)
		}
		fmt.Fprintf(&b, `{%s,"mode":"managed","type":"aws_vpc","name":"main",%s,"instances":[{"schema_version":1,`+
			`"attributes":{"id":"vpc-%d","arn":%q,"cidr_block":"10.%d.0.0/16","enable_dns_support":true,"tags":{"Name":"app-%d"}},`+
			`"sensitive_attributes":[]}]}`, mod, provider, m, arn("vpc", 0), m%256, m)

		fmt.Fprintf(&b, `,{%s,"mode":"managed","type":"aws_subnet","name":"private",%s,"instances":[`, mod, provider)
		for i := 0; i < 9; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"index_key":%d,"schema_version":1,"attributes":{"id":"subnet-%d-%d","arn":%q,`+
				`"vpc_id":"vpc-%d","cidr_block":"10.%d.%d.0/24","map_public_ip_on_launch":false,"tags":{"Name":"private-%d"}},`+
				`"sensitive_attributes":[],"dependencies":["module.app.aws_vpc.main"]}`, i, m, i, arn("subnet", i), m, m%256, i, i)
		}
		b.WriteString(`]}`)

		fmt.Fprintf(&b, `,{%s,"mode":"managed","type":"aws_instance","name":"worker",%s,"instances":[`, mod, provider)
		for i := 0; i < 90; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"index_key":%d,"schema_version":1,"attributes":{"id":"i-%d-%d","arn":%q,`+
				`"subnet_id":"subnet-%d-%d","instance_type":"t3.micro","ami":"ami-0abc","monitoring":false,`+
				`"user_data":"secret-%d-%d","tags":{"Name":"worker-%d"}},`+
				`"sensitive_attributes":[[{"type":"get_attr","value":"user_data"}]],`+
				`"dependencies":["module.app.aws_subnet.private","module.app.aws_vpc.main"]}`,
				i, m, i, arn("instance", i), m, i%9, m, i, i)
		}
		b.WriteString(`]}`)
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

func TestSyntheticStateShape(t *testing.T) {
	g := graph.New()
	if err := Load(g, "synthetic", bytes.NewReader(syntheticState(3))); err != nil {
		t.Fatal(err)
	}
	// Per module: 9 subnets → network, 90 machines → network and the one
	// subnet each machine names.
	if nodes, edges := len(g.Nodes()), len(g.Edges()); nodes != 300 || edges != 3*(9+90*2) {
		t.Errorf("got %d nodes and %d edges, want 300 and %d", nodes, edges, 3*(9+90*2))
	}
}

// BenchmarkLoad100k loads a state of 100,000 resources whose 900,000
// recorded dependencies narrow down to 189,000.
func BenchmarkLoad100k(b *testing.B) {
	data := syntheticState(1000)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g := graph.New()
		if err := Load(g, "synthetic", bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}
