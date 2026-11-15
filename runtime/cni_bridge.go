// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// cni_bridge.go keeps the container bridge's address in line with the node
// subnet (Flow 7, M222): a cca0 left from an earlier subnet makes the CNI
// bridge plugin refuse every container.
package runtime

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
)

// bridgeAddressCommandTimeout bounds one "ip addr del" on the container bridge.
const bridgeAddressCommandTimeout = 5 * time.Second

// ensureBridgeAddressMatchesSubnet removes every IPv4 address from an existing
// bridge except the gateway the CNI bridge plugin assigns for subnetCIDR, so
// the plugin can set it. A missing bridge is fine: the plugin creates it.
func ensureBridgeAddressMatchesSubnet(bridgeName string, subnetCIDR string) error {
	bridgeInterface, lookupError := net.InterfaceByName(bridgeName)
	if lookupError != nil {
		return nil // not created yet; the CNI bridge plugin creates it
	}
	interfaceAddresses, addressError := bridgeInterface.Addrs()
	if addressError != nil {
		return fmt.Errorf("reading %s addresses: %w", bridgeName, addressError)
	}
	currentAddresses := make([]string, 0, len(interfaceAddresses))
	for _, interfaceAddress := range interfaceAddresses {
		currentAddresses = append(currentAddresses, interfaceAddress.String())
	}
	staleAddresses, staleError := staleBridgeAddresses(currentAddresses, subnetCIDR)
	if staleError != nil {
		return staleError
	}
	for _, staleAddress := range staleAddresses {
		if deleteError := deleteBridgeAddress(bridgeName, staleAddress); deleteError != nil {
			return deleteError
		}
		logging.Default().Info("container bridge: removed address from an earlier node subnet",
			"bridge", bridgeName, "address", staleAddress, "subnet", subnetCIDR)
	}
	return nil
}

// deleteBridgeAddress runs "ip addr del address dev bridgeName".
func deleteBridgeAddress(bridgeName string, address string) error {
	commandContext, cancelCommand := context.WithTimeout(context.Background(), bridgeAddressCommandTimeout)
	defer cancelCommand()
	deleteCommand := exec.CommandContext(commandContext, "ip", "addr", "del", address, "dev", bridgeName) //nolint:gosec // address parsed by net.ParseCIDR, bridge name is a constant
	if output, runError := deleteCommand.CombinedOutput(); runError != nil {
		return fmt.Errorf("ip addr del %s dev %s: %w: %s", address, bridgeName, runError, strings.TrimSpace(string(output)))
	}
	return nil
}

// staleBridgeAddresses returns the IPv4 addresses (CIDR form, input order)
// that are not the gateway for subnetCIDR: its first host address with the
// subnet's prefix length. IPv6 and unparsable entries are ignored.
func staleBridgeAddresses(currentAddresses []string, subnetCIDR string) ([]string, error) {
	_, nodeSubnet, parseError := net.ParseCIDR(subnetCIDR)
	if parseError != nil || nodeSubnet.IP.To4() == nil {
		return nil, fmt.Errorf("node subnet %q is not an IPv4 CIDR: %v", subnetCIDR, parseError)
	}
	gatewayAddress := firstHostAddress(nodeSubnet)
	subnetPrefixLength, _ := nodeSubnet.Mask.Size()
	var staleAddresses []string
	for _, currentAddress := range currentAddresses {
		addressIP, addressNetwork, addressError := net.ParseCIDR(currentAddress)
		if addressError != nil || addressIP.To4() == nil {
			continue
		}
		addressPrefixLength, _ := addressNetwork.Mask.Size()
		if addressIP.Equal(gatewayAddress) && addressPrefixLength == subnetPrefixLength {
			continue
		}
		staleAddresses = append(staleAddresses, currentAddress)
	}
	return staleAddresses, nil
}

// firstHostAddress returns the network address of an IPv4 subnet plus one,
// the gateway the CNI bridge plugin assigns with isGateway.
func firstHostAddress(ipv4Subnet *net.IPNet) net.IP {
	networkValue := binary.BigEndian.Uint32(ipv4Subnet.IP.To4())
	gatewayAddress := make(net.IP, net.IPv4len)
	binary.BigEndian.PutUint32(gatewayAddress, networkValue+1)
	return gatewayAddress
}
