/*
Copyright © 2026 oyama
*/
package cmd

import (
	"bytes"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

type Tunnel struct {
	isAttached       bool // flag if we want use already existing tunnel. --attach flag
	PID              int
	CMD              *exec.Cmd
	tunMasqPort      int
	connectionString string
}

var (
	SSHConnectionStrings []string
	facadePort           int
	masqPort             int
	chainName            string
	externalPorts        []int
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

	var tunnels []*Tunnel
	/*
	 * Open new SSH tunnels from flag --ssh
	 */
	for _, connString := range connStrings {

		args := []string{
			"-D", strconv.Itoa(masqPort),
			"-N",
			"-o", "ServerAliveInterval=60",
			"-o", "ServerAliveCountMax=3",
			"-o", "ExitOnForwardFailure=yes",
			"-o", "StrictHostKeyChecking=no",
		}

		address, port := parseSSHConnectionString(connString)
		fmt.Printf("Spinning up SSH Tunnel to %s ...\n", address)
		cmd := exec.Command("ssh", append(args, "-p", strconv.Itoa(port), address)...)

		//cmd.SysProcAttr = &syscall.SysProcAttr{
		//	Setpgid: true,
		//}

		if err := cmd.Start(); err != nil {
			fmt.Printf("Could not spin up SSH Tunnel to %s. Skipping...", address)
		}

		if cmd.Process == nil || cmd.Process.Pid == 0 {
			fmt.Printf("Tunnel died %s. Shit happens...\n", address)
			masqPort++
			continue
		}
		tunnels = append(tunnels, &Tunnel{CMD: cmd, tunMasqPort: masqPort, isAttached: false})

		go func(c *exec.Cmd, addr string) {
			_ = c.Wait()
		}(cmd, address)

		masqPort++
	}

	/*
	 * Check if ports from --attach flag are really open
	 * and create a Tunnel object for them.
	 */
	for _, port := range externalPorts {
		cmd := exec.Command("ss", "-ltn", "sport", "=", strconv.Itoa(port))
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out

		err := cmd.Run()
		if err != nil {
			continue
		}

		output := strings.TrimSpace(out.String())
		lines := strings.Split(output, "\n")

		if len(lines) <= 1 {
			continue
		}

		tunnels = append(tunnels, &Tunnel{isAttached: true, tunMasqPort: port})
	}

	if len(tunnels) == 0 {
		log.Fatalln("No tunnels up. Panik.")
	}
	return tunnels
}

func cleanUpSSHTunnels(tunnels []*Tunnel) {
	for _, t := range tunnels {
		if t.CMD != nil && t.CMD.Process != nil {
			_ = t.CMD.Process.Signal(os.Interrupt)
		}
		log.Printf("Cleaning up PID %d", t.CMD.Process.Pid)
	}
}

func spinUpIpTablesRules(tunnels []*Tunnel) {

	chainName = "MSQI" + strconv.Itoa(rand.IntN((0x270F)+0x3E8)%0x2710)

	fmt.Printf("Creating iptables chain %s\n", chainName)

	// this is blocking op
	_ = exec.Command("iptables", "-t", "nat", "-N", chainName).Run()

	err := exec.Command("iptables", "-t", "nat", "-A", "OUTPUT",
		"-d", "127.0.0.1",
		"-o", "lo",
		"-p", "tcp",
		"--dport", strconv.Itoa(facadePort),
		"-j", chainName).Run()
	if err != nil {
		log.Fatalf("Failed to add OUTPUT jump rule: %v", err)
	}

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

		fmt.Printf("Applying iptables rule %s ...\n", args)

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
	_ = exec.Command("iptables", "-t", "nat", "-X", chainName).Run()
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
		defer func() {
			cleanUpIpTablesRules()
			cleanUpSSHTunnels(tunnels)
		}()

		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs

	},
}

func init() {
	rootCmd.AddCommand(proxyCmd)
	proxyCmd.Flags().IntVarP(&facadePort, "port", "p", 1337, "facade (listen) port")
	proxyCmd.Flags().StringSliceVarP(&SSHConnectionStrings, "ssh", "s", []string{}, "sshstring")
	proxyCmd.Flags().IntSliceVarP(&externalPorts, "attach", "a", []int{}, "existings ssh tunnels ports to attach")
}
