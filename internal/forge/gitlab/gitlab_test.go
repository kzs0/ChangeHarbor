package gitlab
import "testing"
func TestUnsupportedApprovalCannotSilentlySucceed(t *testing.T){c:=Capabilities();if !c.RevisionBoundApproval&&RequireApprovalCapability()==nil{t.Fatal("unsupported approval accepted")}}
