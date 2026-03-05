/*
Copyright © 2026 oyama
*/
package cmd

import (
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	//"golang.org/x/crypto/ssh"
	//"net"

	//"github.com/armon/go-socks5"
	"github.com/spf13/cobra"
	//"golang.org/x/net/context"
)

type Tunnel struct {
	PID              int
	tunMasqPort      int
	connectionString string
}

var (
	SSHConnectionStrings []string
	facadePort           int
	masqPort             int
	chainName            string
)

func parseSSHConnectionString(connString string) (string, int) {
	var address string = ""
	var port int = 0
	if len(strings.Split(connString, ":")) < 2 {
		address = connString
		port = 22
	} else {
		portSplit := strings.Split(connString, ":")
		address = portSplit[0]
		port, _ = strconv.Atoi(portSplit[1])
	}
	return address, port
}

func spinUpSSHTunnels(connStrings []string) []*Tunnel {
	args := []string{
		"-D", strconv.Itoa(masqPort),
		"-N", "-f",
		"-o", "ServerAliveInterval=60",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StrictHostKeyChecking=no",
	}

	masqPort++

	var tunnels []*Tunnel
	for _, connString := range connStrings {
		address, port := parseSSHConnectionString(connString)
		cmd := exec.Command("ssh", append(args, "-p", strconv.Itoa(port), address)...)

		if err := cmd.Start(); err != nil {
			fmt.Printf("Could not spin up SSH Tunnel to %s. Skipping...", address)
		}

		time.Sleep(800 * time.Millisecond)

		if cmd.Process == nil || cmd.Process.Pid == 0 {
			fmt.Printf("Tunnel died %s. Shit happens...", address)
		}
		tunnels = append(tunnels, &Tunnel{PID: cmd.Process.Pid, tunMasqPort: masqPort})
	}

	if len(tunnels) == 0 {
		log.Fatalln("No tunnels up. Panik.")
	}
	return tunnels
}

func cleanUpSSHTunnels(tunnels []*Tunnel) {
	for _, t := range tunnels {
		if t.PID > 0 {
			_ = syscall.Kill(t.PID, syscall.SIGTERM)
			log.Printf("Cleaning up PID %d", t.PID)
		}
	}
}

func spinUpIpTablesRules(tunnels []*Tunnel) {

	chainName = "MSQI" + strconv.Itoa(rand.IntN((0x270F)+0x3E8)%0x2710)

	fmt.Printf("Creating iptables chain %s", chainName)

	// this is blocking op
	_ = exec.Command("iptables", "-t", "nat", "-N", chainName).Run()

	for i, tun := range tunnels {

		args := []string{
			"-t", "nat",
			"-A", chainName,
			"-d", "127.0.0.1",
			"-o", "lo",
			"-p", "tcp",
			"--dport", strconv.Itoa(facadePort),
			"-j", "DNAT",
			"--to-destination", "127.0.0.1:" + strconv.Itoa(tun.tunMasqPort),
		}

		if i < len(tunnels)-1 {
			args = append(args,
				"-m",
				"statistic",
				"--mode",
				"nth",
				"--every",
				strconv.Itoa(len(tunnels)-i),
				"--packet",
				"0")
		}

		fmt.Printf("Applying iptables rule %s \n...", args)

		cmd := exec.Command("iptables", args...)
		if err := cmd.Run(); err != nil {
			log.Fatalf("Could not apply iptables rule. Exiting...")
		}

	}

}

func cleanUpIpTablesRules() {

	fmt.Println("Cleaning up iptables rules all the shit...")
	_ = exec.Command("iptables", "-t", "nat", "-D", "OUTPUT",
		"-p", "tcp", "--dport", strconv.Itoa(facadePort),
		"-j", chainName).Run()

	_ = exec.Command("iptables", "-t", "nat", "-F", chainName).Run()
	_ = exec.Command("iptabes", "-t", "nat", "-X", chainName).Run()
}

// proxyCmd represents the proxy command
var proxyCmd = &cobra.Command{
	Use:   "proxy",
	Short: "Single socks5 <-> ssh tunnel",
	Run: func(cmd *cobra.Command, args []string) {

		// Check euid == 0
		if os.Geteuid() != 0 {
			log.Fatalln("Not enough privileges. Run with sudo/suid")
		}

		// Setup initial masqPort
		masqPort = 13370

		tunnels := spinUpSSHTunnels(SSHConnectionStrings)
		spinUpIpTablesRules(tunnels)
		defer cleanUpIpTablesRules()
		defer cleanUpSSHTunnels(tunnels)

	},
}

func init() {
	rootCmd.AddCommand(proxyCmd)
	proxyCmd.Flags().IntVarP(&facadePort, "port", "p", 1337, "facade (listen) port")
	proxyCmd.Flags().StringSliceVarP(&SSHConnectionStrings, "ssh", "s", []string{}, "sshstring")
}
