package dispatcher

import (
	"context"
	"testing"

	"github.com/xtls/xray-core/common"
	xnet "github.com/xtls/xray-core/common/net"
)

func TestTrafficBudgetSuspensionRejectsNewLinksUntilServerResume(t *testing.T) {
	dispatcher := &DefaultDispatcher{}
	dispatcher.SetTrafficSuspended(true)
	if _, _, _, err := dispatcher.getLink(context.Background(), xnet.Network_TCP); err == nil {
		t.Fatal("getLink() accepted traffic while the server budget was suspended")
	}

	dispatcher.SetTrafficSuspended(false)
	inbound, outbound, _, err := dispatcher.getLink(context.Background(), xnet.Network_TCP)
	if err != nil {
		t.Fatalf("getLink() after resume error = %v", err)
	}
	common.Interrupt(inbound.Reader)
	common.Interrupt(outbound.Reader)
}
