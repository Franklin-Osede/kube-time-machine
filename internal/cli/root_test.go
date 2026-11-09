package cli

import (
	"bytes"
	"strings"
	"testing"
)

// Errors from argument parsing must name the argument the command actually
// takes, and an unsupported rollback kind must be refused before storage or
// the cluster is touched.
func TestRootCmd_ArgumentErrors(t *testing.T) {
	missing := t.TempDir() + "/does-not-exist"
	for _, tc := range []struct {
		args    []string
		want    string
		notWant string
	}{
		{[]string{"blame", "foo"}, "expected <kind>/<namespace>/<name>", "--key"},
		{[]string{"rollback", "foo", "--to", "x"}, "expected <kind>/<namespace>/<name>", "--key"},
		{[]string{"rollback", "pod/a/b", "--to", "x"}, "supports only Deployment and ConfigMap", "storage"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			cmd := NewRootCmd("test")
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(append([]string{"--storage-dir", missing}, tc.args...))
			err := cmd.Execute()
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
			if strings.Contains(err.Error(), tc.notWant) {
				t.Errorf("error %q mentions %q", err, tc.notWant)
			}
		})
	}
}
