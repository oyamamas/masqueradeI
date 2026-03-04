/*
Copyright © 2026 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	//"context"
	"fmt"
	"strconv"
	"strings"

	//"golang.org/x/crypto/ssh"
	//"net"

	//"github.com/armon/go-socks5"
	"github.com/spf13/cobra"
	//"golang.org/x/net/context"
)

var (
	SSHConnectionStrings []string
)

// proxyCmd represents the proxy command
var proxyCmd = &cobra.Command{
	Use:   "proxy",
	Short: "Single socks5 <-> ssh tunnel",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Spinning up SSH Tunnels")

		//
		// ssh-agent here
		//

		//sshConfig := &ssh.ClientConfig{
		//	Config:          ssh.Config{},
		//	User:            "",
		//	Auth:            nil,
		//	HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		//}
		fmt.Println(parseSSHConnectionString(SSHConnectionStrings[0]))

		fmt.Println("Starting SOCK5 listen server %")

		//socksConfig := &socks5.Config{
		//	Rules: socks5.PermitAll(),
		//	Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		//		return nil, nil
		//	},
		//}

	},
}

func parseSSHConnectionString(ConnectionString string) (string, string, int) {
	UserNameSplited := strings.Split(ConnectionString, "@")
	var user string = ""
	var address string = ""
	var port int = 0

	if len(strings.Split(UserNameSplited[1], ":")) < 2 {
		user = UserNameSplited[0]
		address = UserNameSplited[1]
		port = 22
	} else {
		user = UserNameSplited[0]
		PortSplited := strings.Split(UserNameSplited[1], ":")
		address = PortSplited[0]
		port, _ = strconv.Atoi(PortSplited[1])
	}
	return user, address, port
}

func init() {
	rootCmd.AddCommand(proxyCmd)
	proxyCmd.Flags().StringSliceVarP(&SSHConnectionStrings, "ssh", "s", []string{}, "sshstring")
	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// proxyCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// proxyCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
