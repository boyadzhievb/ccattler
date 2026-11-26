// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// etcdPeerFirewallSyncUnit is the name of the timer and of the service it runs
// that re-apply the etcd peer port firewall (M280, user decision "go for a").
const etcdPeerFirewallSyncUnit = "ccattler-etcd-peer-firewall-sync"

// etcdPeerFirewallSyncWantInterval is the re-apply interval the user decided.
const etcdPeerFirewallSyncWantInterval = "30s"

// etcdPeerFirewallSyncUnitDirectory is where the role installs the sync units.
const etcdPeerFirewallSyncUnitDirectory = "/etc/systemd/system/"

// nftablesServicePattern matches any mention of the nftables service (unit
// name or "systemctl … nftables"); the etcd-cluster role must never touch it.
var nftablesServicePattern = regexp.MustCompile(`(?i)\bnftables\b`)

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
	const serviceTaskName = "Install the service that re-applies the etcd peer port firewall"
	const timerTaskName = "Install the timer that re-applies the etcd peer port firewall"
	// CR7-F2: the units the start task names are the files the copy tasks install.
	for taskName, wantDestination := range map[string]string{serviceTaskName: etcdPeerFirewallSyncUnit + ".service", timerTaskName: etcdPeerFirewallSyncUnit + ".timer"} {
		if destination := ansibleTaskField(t, roleTasksPath, taskName, "copy", "dest"); destination != etcdPeerFirewallSyncUnitDirectory+wantDestination {
			t.Errorf("task %q installs %v, want %s%s", taskName, destination, etcdPeerFirewallSyncUnitDirectory, wantDestination)
		}
	}
	serviceText, _ := ansibleTaskField(t, roleTasksPath, serviceTaskName, "copy", "content").(string)
	serviceSections := systemdUnitSections(renderAnsibleText(t, serviceText, "three_ca", roleDefaults))
	requireUnitSettings(t, etcdPeerFirewallSyncUnit+".service", serviceSections,
		map[string][]string{"Unit": {}, "Service": {"Type=oneshot", "ExecStart=" + firewallCommand,
			"TimeoutStartSec=" + fmt.Sprint(roleDefaults["etcd_peer_firewall_sync_start_timeout"])}})
	requireStartTimeoutBelowInterval(t, roleDefaults)
	for sectionName, sectionLines := range serviceSections {
		for _, sectionLine := range sectionLines {
			if strings.HasPrefix(sectionLine, "RemainAfterExit=") {
				t.Errorf("%s.service [%s] has %s: the timer would run it only once", etcdPeerFirewallSyncUnit, sectionName, sectionLine)
			}
		}
	}
	timerText, _ := ansibleTaskField(t, roleTasksPath, timerTaskName, "copy", "content").(string)
	timerSections := systemdUnitSections(renderAnsibleText(t, timerText, "three_ca", roleDefaults))
	requireUnitSettings(t, etcdPeerFirewallSyncUnit+".timer", timerSections,
		map[string][]string{"Unit": {}, "Timer": {"OnBootSec=" + etcdPeerFirewallSyncWantInterval,
			"OnUnitActiveSec=" + etcdPeerFirewallSyncWantInterval, "AccuracySec=1s"}, "Install": {"WantedBy=timers.target"}})
	// SA7-L2: the timer runs the service of its own name, never another unit (the
	// boot unit has RemainAfterExit and would run only once).
	for sectionName, sectionLines := range timerSections {
		for _, sectionLine := range sectionLines {
			if strings.HasPrefix(sectionLine, "Unit=") {
				t.Errorf("%s.timer [%s] has %s: it must run %s.service", etcdPeerFirewallSyncUnit, sectionName, sectionLine, etcdPeerFirewallSyncUnit)
			}
		}
	}
	// The nft template's header names the interval from the same role default.
	firewallRules := runAnsibleRenderWithVariables(t, "template", filepath.Join(roleDirectory, "templates", "etcd-peer-firewall.nft.j2"),
		"three_ca", "ctrl", roleDefaults)
	if !strings.Contains(firewallRules, "every\n# "+etcdPeerFirewallSyncWantInterval+" by") {
		t.Errorf("etcd-peer-firewall.nft.j2 does not name the %s interval:\n%s", etcdPeerFirewallSyncWantInterval, firewallRules)
	}
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

// requireStartTimeoutBelowInterval (SA7-L1) fails the test unless the sync
// service's start timeout is shorter than the timer interval, so a hung nft
// is killed before the next tick instead of stopping the timer.
func requireStartTimeoutBelowInterval(t *testing.T, roleDefaults map[string]any) {
	t.Helper()
	startTimeout, timeoutError := time.ParseDuration(fmt.Sprint(roleDefaults["etcd_peer_firewall_sync_start_timeout"]))
	syncInterval, intervalError := time.ParseDuration(fmt.Sprint(roleDefaults["etcd_peer_firewall_sync_interval"]))
	if timeoutError != nil || intervalError != nil || startTimeout <= 0 || startTimeout >= syncInterval {
		t.Errorf("etcd_peer_firewall_sync_start_timeout %v must be a positive duration below the interval %v (%v, %v)",
			roleDefaults["etcd_peer_firewall_sync_start_timeout"], roleDefaults["etcd_peer_firewall_sync_interval"], timeoutError, intervalError)
	}
}

// TestEtcdRoleLeavesNftablesServiceAlone (user decision "go for a", SA7-L3,
// CR7-F3): no task of the role (module, command, shell or unit text) mentions
// nftables or a ruleset flush, and the reload unit is gone.
func TestEtcdRoleLeavesNftablesServiceAlone(t *testing.T) {
	roleTasksPath := filepath.Join(ansibleRolesDirectory, "etcd-cluster", "tasks", "main.yml")
	var taskList []map[string]any
	if decodeError := yaml.Unmarshal(mustReadFile(t, roleTasksPath), &taskList); decodeError != nil {
		t.Fatal(decodeError)
	}
	for _, task := range taskList {
		taskYAML, _ := yaml.Marshal(task)
		if nftablesMention := nftablesServicePattern.FindString(string(taskYAML)); nftablesMention != "" {
			t.Errorf("task %q mentions %q: the role must never enable, start or depend on nftables.service", task["name"], nftablesMention)
		}
		for _, forbiddenText := range []string{"flush ruleset", "peer-firewall-reload", "PartOf=", "ReloadPropagatedFrom="} {
			if strings.Contains(string(taskYAML), forbiddenText) {
				t.Errorf("task %q has %q (the timer replaced the reload unit; a ruleset flush would wipe the agent's tables)",
					task["name"], forbiddenText)
			}
		}
	}
}
