package node

import (
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
)

func TestCompareUserListDetectsUpdatedUser(t *testing.T) {
	oldUsers := []panel.UserInfo{
		{Id: 1, Uuid: "u1", SpeedLimit: 10, DeviceLimit: 1},
		{Id: 2, Uuid: "u2", SpeedLimit: 20, DeviceLimit: 2},
	}
	newUsers := []panel.UserInfo{
		{Id: 1, Uuid: "u1", SpeedLimit: 10, DeviceLimit: 3},
		{Id: 3, Uuid: "u3", SpeedLimit: 30, DeviceLimit: 1},
	}

	deleted, added, updated := compareUserList(oldUsers, newUsers)
	if len(deleted) != 1 || deleted[0].Uuid != "u2" {
		t.Fatalf("deleted = %#v, want only u2", deleted)
	}
	if len(added) != 1 || added[0].Uuid != "u3" {
		t.Fatalf("added = %#v, want only u3", added)
	}
	if len(updated) != 1 || updated[0].Uuid != "u1" || updated[0].DeviceLimit != 3 {
		t.Fatalf("updated = %#v, want u1 device_limit=3", updated)
	}
}

func TestFilterReportedOnlineUsersOnlyKeepsUsersWithTraffic(t *testing.T) {
	onlineUsers := []panel.OnlineUser{
		{UID: 1, IP: "1.1.1.1"},
		{UID: 2, IP: "2.2.2.2"},
		{UID: 3, IP: "3.3.3.3"},
	}
	userTraffic := []panel.UserTraffic{
		{UID: 1, Upload: 128, Download: 0},
		{UID: 2, Upload: 0, Download: 0},
	}

	got := filterReportedOnlineUsers(onlineUsers, userTraffic)
	if len(got) != 1 || got[0].UID != 1 {
		t.Fatalf("filterReportedOnlineUsers() = %#v, want only UID 1", got)
	}
}
