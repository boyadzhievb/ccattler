// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Flag parsing shared by the commands that configure a node: server, agent, init.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// newDaemonFlagSet returns the flag set for `cca <commandName>`. It reports
// errors to the caller and prints nothing itself.
func newDaemonFlagSet(commandName string) *flag.FlagSet {
	flagSet := flag.NewFlagSet("cca "+commandName, flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	return flagSet
}

// parseDaemonFlagSet parses args. An unknown flag, a flag without its value
// and a positional argument are errors, never ignored (M227). For --help it
// prints the flag list and returns flag.ErrHelp.
func parseDaemonFlagSet(flagSet *flag.FlagSet, args []string) error {
	positionalArguments, parseError := parseFlagsAndPositionals(flagSet, args)
	if parseError != nil {
		return parseError
	}
	if len(positionalArguments) > 0 {
		return fmt.Errorf("%s: unexpected argument %q (run '%s --help' for the flag list)",
			flagSet.Name(), positionalArguments[0], flagSet.Name())
	}
	return nil
}

// parseFlagsAndPositionals parses flags standing before, between or after
// positional arguments and returns the positionals in order. An unknown flag
// or a flag without its value is an error; --help prints the flag list.
func parseFlagsAndPositionals(flagSet *flag.FlagSet, args []string) ([]string, error) {
	var positionalArguments []string
	remainingArguments := args
	for {
		parseError := flagSet.Parse(remainingArguments)
		if errors.Is(parseError, flag.ErrHelp) {
			printDaemonFlagList(flagSet)
			return nil, flag.ErrHelp
		}
		if parseError != nil {
			return nil, fmt.Errorf("%s: %s (run '%s --help' for the flag list)",
				flagSet.Name(), spellFlagsWithTwoDashes(parseError.Error()), flagSet.Name())
		}
		if flagSet.NArg() == 0 {
			return positionalArguments, nil
		}
		// After "--" nothing is a flag: all that follows is positional.
		consumedArguments := remainingArguments[:len(remainingArguments)-flagSet.NArg()]
		if len(consumedArguments) > 0 && consumedArguments[len(consumedArguments)-1] == "--" {
			return append(positionalArguments, flagSet.Args()...), nil
		}
		// The flag package stops at the first positional; take it and go on.
		positionalArguments = append(positionalArguments, flagSet.Arg(0))
		remainingArguments = flagSet.Args()[1:]
	}
}

// printDaemonFlagList prints every flag of the command with its help text and
// default, spelled with two dashes as in the documentation.
func printDaemonFlagList(flagSet *flag.FlagSet) {
	fmt.Printf("usage: %s [flags]\n\nFlags:\n", flagSet.Name())
	flagSet.VisitAll(func(registeredFlag *flag.Flag) {
		valueName, helpText := flag.UnquoteUsage(registeredFlag)
		flagLine := "  --" + registeredFlag.Name
		if valueName != "" {
			flagLine += " <" + valueName + ">"
		}
		if registeredFlag.DefValue != "" && registeredFlag.DefValue != "false" {
			helpText += " (default " + registeredFlag.DefValue + ")"
		}
		fmt.Printf("%s\n        %s\n", flagLine, helpText)
	})
}

// spellFlagsWithTwoDashes rewrites the flag package's "-name" in an error
// message to "--name", the spelling users type and the documentation uses.
func spellFlagsWithTwoDashes(flagErrorMessage string) string {
	for _, messagePrefix := range []string{"not defined: ", "needs an argument: ", "for flag ", "\" for "} {
		flagErrorMessage = strings.Replace(flagErrorMessage, messagePrefix+"-", messagePrefix+"--", 1)
	}
	return flagErrorMessage
}

// exitOnDaemonFlagError ends the process when flag parsing failed: status 0
// after --help, otherwise status 1 with the error on stderr.
func exitOnDaemonFlagError(parseError error) {
	if parseError == nil {
		return
	}
	if errors.Is(parseError, flag.ErrHelp) {
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "error: %v\n", parseError)
	os.Exit(1)
}

// registerStoreFlags registers the state store flags shared by server and
// agent. --endpoints also selects the etcd backend; a later --store wins.
func registerStoreFlags(flagSet *flag.FlagSet, storeBackend *string, etcdEndpoints *string, storeKeyPrefix *string) {
	flagSet.StringVar(storeBackend, "store", *storeBackend, "state store backend: memory or etcd")
	flagSet.StringVar(storeKeyPrefix, "store-prefix", *storeKeyPrefix, "key prefix inside a shared etcd cluster")
	flagSet.Func("endpoints", "comma-separated etcd addresses (selects the etcd store)", func(flagValue string) error {
		*etcdEndpoints = flagValue
		*storeBackend = "etcd"
		return nil
	})
}

// tlsFileFlagTargets names the configuration fields that receive one set of
// TLS file paths, so the three paths cannot be passed in the wrong order.
type tlsFileFlagTargets struct {
	certificatePath   *string // certificatePath receives the PEM certificate path.
	privateKeyPath    *string // privateKeyPath receives the PEM private key path.
	caCertificatePath *string // caCertificatePath receives the PEM CA certificate path.
}

// registerCertificateFlags registers the node's own TLS files and the etcd
// client TLS files, shared by server and agent.
func registerCertificateFlags(flagSet *flag.FlagSet, nodeTLSFiles tlsFileFlagTargets, etcdTLSFiles tlsFileFlagTargets) {
	flagSet.StringVar(nodeTLSFiles.certificatePath, "cert", "", "PEM certificate of this node (with --key and --ca)")
	flagSet.StringVar(nodeTLSFiles.privateKeyPath, "key", "", "PEM private key of this node")
	flagSet.StringVar(nodeTLSFiles.caCertificatePath, "ca", "", "PEM cluster CA certificate")
	flagSet.StringVar(etcdTLSFiles.certificatePath, "etcd-cert", "", "PEM client certificate for etcd")
	flagSet.StringVar(etcdTLSFiles.privateKeyPath, "etcd-key", "", "PEM client private key for etcd")
	flagSet.StringVar(etcdTLSFiles.caCertificatePath, "etcd-ca", "", "PEM CA certificate that signed the etcd servers")
}

// registerLogFlags registers the logging flags shared by server and agent.
func registerLogFlags(flagSet *flag.FlagSet, logLevel *string, logFormat *string) {
	flagSet.StringVar(logLevel, "log-level", "", "minimum log severity: debug, info, warn or error")
	flagSet.StringVar(logFormat, "log-format", "", "log output: human or json")
}
