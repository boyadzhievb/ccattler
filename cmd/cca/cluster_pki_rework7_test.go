// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// etcdPeerFirewallSyncUnit is the name of the timer and of the service it runs
// that re-apply the etcd peer port firewall (M280, user decision "go for a").
const etcdPeerFirewallSyncUnit = "ccattler-etcd-peer-firewall-sync"

// etcdPeerFirewallSyncWantInterval is the re-apply interval the user decided.
const etcdPeerFirewallSyncWantInterval = "30s"

// etcdPeerFirewallCommand returns the nft command of the boot firewall unit,
// which the sync service and etcd's ExecStartPre must run unchanged.
func etcdPeerFirewallCommand(t *testing.T, roleTasksPath string) string {
	t.Helper()
	firewallUnitText, _ := ansibleTaskField(t, roleTasksPath, "Install the unit that loads the etcd peer port firewall at boot", "copy", "content").(string)
	_, firewallUnitExec, _ := strings.Cut(firewallUnitText, "ExecStart=")
	firewallUnitExec, _, _ = strings.Cut(firewallUnitExec, "\n")
	if firewallUnitExec == "" {
		t.Fatalf("the boot firewall unit has no ExecStart:\n%s", firewallUnitText)
	}
	return firewallUnitExec
}

// systemdUnitSections splits a unit file into its sections: section name
// ("Unit", "Service", ...) to its non-empty, non-comment lines.
func systemdUnitSections(unitText string) map[string][]string {
	unitSections := map[string][]string{}
	currentSection := ""
	for _, unitLine := range strings.Split(unitText, "\n") {
		unitLine = strings.TrimSpace(unitLine)
		switch {
		case unitLine == "" || strings.HasPrefix(unitLine, "#") || strings.HasPrefix(unitLine, ";"):
		case strings.HasPrefix(unitLine, "[") && strings.HasSuffix(unitLine, "]"):
			currentSection = strings.Trim(unitLine, "[]")
			if _, sectionSeen := unitSections[currentSection]; !sectionSeen {
				unitSections[currentSection] = []string{}
			}
		default:
			unitSections[currentSection] = append(unitSections[currentSection], unitLine)
		}
	}
	return unitSections
}

// requireUnitSettings fails the test for each wanted section the unit lacks
// and each wanted "Key=value" line it does not have in that section.
func requireUnitSettings(t *testing.T, unitName string, unitSections map[string][]string, wantSettings map[string][]string) {
	t.Helper()
	for _, sectionName := range slices.Sorted(maps.Keys(wantSettings)) {
		if _, sectionPresent := unitSections[sectionName]; !sectionPresent {
			t.Errorf("%s has no [%s] section", unitName, sectionName)
		}
		for _, wantLine := range wantSettings[sectionName] {
			if !slices.Contains(unitSections[sectionName], wantLine) {
				t.Errorf("%s [%s] lacks %q: %v", unitName, sectionName, wantLine, unitSections[sectionName])
			}
		}
	}
}

// TestEtcdPeerFirewallSyncTimer (SA4-L4, SA5-L2, user decision "go for a"): a
// timer re-applies the peer firewall every 30 s (and after boot) through a
// oneshot service without RemainAfterExit, so every tick runs nft again; the
// role enables and starts the timer, and the reload unit is gone.
func TestEtcdPeerFirewallSyncTimer(t *testing.T) {
	roleDirectory := filepath.Join(ansibleRolesDirectory, "etcd-cluster")
	roleTasksPath := filepath.Join(roleDirectory, "tasks", "main.yml")
	roleDefaults := map[string]any{}
	if decodeError := yaml.Unmarshal(mustReadFile(t, filepath.Join(roleDirectory, "defaults", "main.yml")), &roleDefaults); decodeError != nil {
		t.Fatal(decodeError)
	}
	if roleDefaults["etcd_peer_firewall_sync_interval"] != etcdPeerFirewallSyncWantInterval {
		t.Errorf("etcd_peer_firewall_sync_interval = %v, want %s", roleDefaults["etcd_peer_firewall_sync_interval"], etcdPeerFirewallSyncWantInterval)
	}
	firewallCommand := etcdPeerFirewallCommand(t, roleTasksPath)
	serviceText, _ := ansibleTaskField(t, roleTasksPath, "Install the service that re-applies the etcd peer port firewall", "copy", "content").(string)
	serviceSections := systemdUnitSections(renderAnsibleText(t, serviceText, "three_ca", roleDefaults))
	requireUnitSettings(t, etcdPeerFirewallSyncUnit+".service", serviceSections,
		map[string][]string{"Unit": {}, "Service": {"Type=oneshot", "ExecStart=" + firewallCommand}})
	for sectionName, sectionLines := range serviceSections {
		for _, sectionLine := range sectionLines {
			if strings.HasPrefix(sectionLine, "RemainAfterExit=") {
				t.Errorf("%s.service [%s] has %s: the timer would run it only once", etcdPeerFirewallSyncUnit, sectionName, sectionLine)
			}
		}
	}
	timerText, _ := ansibleTaskField(t, roleTasksPath, "Install the timer that re-applies the etcd peer port firewall", "copy", "content").(string)
	requireUnitSettings(t, etcdPeerFirewallSyncUnit+".timer", systemdUnitSections(renderAnsibleText(t, timerText, "three_ca", roleDefaults)),
		map[string][]string{"Unit": {}, "Timer": {"OnBootSec=" + etcdPeerFirewallSyncWantInterval,
			"OnUnitActiveSec=" + etcdPeerFirewallSyncWantInterval, "AccuracySec=1s"}, "Install": {"WantedBy=timers.target"}})
	const startTaskName = "Start the etcd peer port firewall timer now and at boot"
	if timerName := ansibleTaskField(t, roleTasksPath, startTaskName, "systemd", "name"); timerName != etcdPeerFirewallSyncUnit+".timer" {
		t.Errorf("task %q manages %v, want %s.timer", startTaskName, timerName, etcdPeerFirewallSyncUnit)
	}
	if enabled, _ := ansibleTaskField(t, roleTasksPath, startTaskName, "systemd", "enabled").(bool); !enabled {
		t.Errorf("%s.timer is not enabled", etcdPeerFirewallSyncUnit)
	}
	if timerState := ansibleTaskField(t, roleTasksPath, startTaskName, "systemd", "state"); timerState != "started" {
		t.Errorf("%s.timer state %v, want started", etcdPeerFirewallSyncUnit, timerState)
	}
}

// TestEtcdRoleLeavesNftablesServiceAlone (user decision "go for a"): the role
// never enables, starts or follows nftables.service, whose "flush ruleset"
// would wipe the cca agent's tables, and no longer installs the reload unit.
func TestEtcdRoleLeavesNftablesServiceAlone(t *testing.T) {
	roleTasksPath := filepath.Join(ansibleRolesDirectory, "etcd-cluster", "tasks", "main.yml")
	var taskList []map[string]any
	if decodeError := yaml.Unmarshal(mustReadFile(t, roleTasksPath), &taskList); decodeError != nil {
		t.Fatal(decodeError)
	}
	for _, task := range taskList {
		for _, serviceModule := range []string{"systemd", "service", "ansible.builtin.systemd", "ansible.builtin.service"} {
			moduleArguments, _ := task[serviceModule].(map[string]any)
			if unitName, _ := moduleArguments["name"].(string); strings.HasPrefix(unitName, "nftables") {
				t.Errorf("task %q manages %s", task["name"], unitName)
			}
		}
		taskYAML, _ := yaml.Marshal(task)
		for _, removedSetting := range []string{"peer-firewall-reload", "PartOf=nftables", "ReloadPropagatedFrom=", "WantedBy=nftables"} {
			if strings.Contains(string(taskYAML), removedSetting) {
				t.Errorf("task %q still has %q (the reload unit was replaced by the timer)", task["name"], removedSetting)
			}
		}
	}
}
