package node

import "testing"

func TestNodeCloseIgnoresUnstartedControllers(t *testing.T) {
	n := &Node{
		controllers: []*Controller{
			{},
			nil,
			{},
		},
	}

	if err := n.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if n.controllers != nil {
		t.Fatalf("controllers = %#v, want nil after close", n.controllers)
	}
}
