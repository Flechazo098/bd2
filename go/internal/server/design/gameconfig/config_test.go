package gameconfig

import (
	"testing"

	commonconfig "bd2server/internal/common/config"
)

func TestGameConfigurationProblemsAreLocatedByField(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want []string
	}{
		{name: "omitted groups keep defaults", raw: `{"schema_version":1}`},
		{name: "deployed server configuration", raw: `{"schema_version":1,"gacha":{"include_collaboration_ur_weapons":false},"story":{"start_pack_id":21},
			"purchases":{"diamond_recharge":{"currency":"free","gold_per_paid_diamond":1000,"diamonds_per_paid_diamond":1}},
			"realtime":{"enabled":true,"tcp_listen":"0.0.0.0:8444","udp_listen":"0.0.0.0:8444","public_host":"game.example.com","tcp_port":8444,"udp_port":8444,
				"tls_cert_file":"/app/tls/server.crt","tls_key_file":"/app/tls/server.key","development_plain_loopback":false},
			"text_moderation":{"rule_files":["moderation/words.txt"]}}`},
		{name: "format version is required", raw: `{}`, want: []string{"schema_version"}},
		{name: "empty currency still forbids recharge", raw: `{"schema_version":1,"purchases":{"diamond_recharge":{"currency":""}}}`},
		{name: "start pack and rates", raw: `{"schema_version":1,"story":{"start_pack_id":2},"purchases":{"diamond_recharge":{"currency":"gold","gold_per_paid_diamond":0}}}`,
			want: []string{"story.start_pack_id", "purchases.diamond_recharge.gold_per_paid_diamond"}},
		{name: "disabled realtime ignores incomplete fields", raw: `{"schema_version":1,"realtime":{"enabled":false,"tcp_port":0}}`},
		{name: "enabled realtime requires its listeners and TLS", raw: `{"schema_version":1,"realtime":{"enabled":true,"public_host":"a b","tcp_port":70000,"udp_port":8444,"tcp_listen":"bad","udp_listen":"0.0.0.0:8444"}}`,
			want: []string{"realtime.public_host", "realtime.tcp_port", "realtime.tcp_listen", "realtime.tls_cert_file", "realtime.tls_key_file"}},
		{name: "plaintext development binds loopback", raw: `{"schema_version":1,"realtime":{"enabled":true,"development_plain_loopback":true,"public_host":"game.example.com","tcp_port":1,"udp_port":1,"tcp_listen":"0.0.0.0:1","udp_listen":"127.0.0.1:1"}}`,
			want: []string{"realtime.public_host", "realtime.tcp_listen"}},
		{name: "moderation provider", raw: `{"schema_version":1,"text_moderation":{"rule_files":[""],"provider":{"type":"azure_content_safety","scopes":["chat","chat","Bad"],"timeout_ms":5,"failure_policy":"deny","severity_threshold":4}}}`,
			want: []string{"text_moderation.rule_files[0]", "text_moderation.provider.endpoint", "text_moderation.provider.api_key_env",
				"text_moderation.provider.scopes[1]", "text_moderation.provider.scopes[2]", "text_moderation.provider.timeout_ms"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Document.Parse([]byte(test.raw))
			got := map[string]bool{}
			for _, problem := range commonconfig.AsProblems(err) {
				got[problem.Path] = true
			}
			for _, path := range test.want {
				if !got[path] {
					t.Errorf("missing problem at %s; got %v", path, err)
				}
			}
			if len(got) != len(test.want) {
				t.Errorf("problems %v, want exactly %v", err, test.want)
			}
		})
	}
}
