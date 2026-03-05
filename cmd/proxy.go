/*
Copyright © 2026 oyama
*/
package cmd

import (
	"log"
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
	localPort        int
	connectionString string
}

var (
	SSHConnectionStrings []string
	facadePort           int16
	masqPort             int
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

func cleanUpSSHTunnels(tunnels []*Tunnel) {
	for _, t := range tunnels {
		if t.PID > 0 {
			_ = syscall.Kill(t.PID, syscall.SIGTERM)
			log.Printf("Остановлен ssh → (pid %d)", t.PID)
		}
	}
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
			log.Fatalln("AAAAAS")
		}

		time.Sleep(800 * time.Millisecond)

		if cmd.Process == nil || cmd.Process.Pid == 0 {
			log.Fatalln("AAAAAAAAAAASSSS")
		}
		tunnels = append(tunnels, &Tunnel{PID: cmd.Process.Pid, localPort: masqPort})
	}
	return tunnels
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
		defer cleanUpSSHTunnels(tunnels)

	},
}

func init() {
	rootCmd.AddCommand(proxyCmd)
	proxyCmd.Flags().Int16VarP(&facadePort, "port", "p", 1337, "facade (listen) port")
	proxyCmd.Flags().StringSliceVarP(&SSHConnectionStrings, "ssh", "s", []string{}, "sshstring")
	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// proxyCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// proxyCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
