package cmd

import (
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	"github.com/gerdou/terralint/cmd/utilities"
	"github.com/gerdou/terralint/version"
	"os"
)

var terraformPath string
var terraformFilePath string
var terraformDirectoryPath string
var versionFlag bool

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "terralint",
	Short: "Terraform Linter",
	Long:  `Checks and lint terraform files based on an opinionated style guide.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if versionFlag {
			fmt.Println(version.Version)
			return nil
		}
		if terraformFilePath != "" || terraformDirectoryPath != "" {
			return errors.New("the -f and -d flags need a command: check or apply")
		}

		return cmd.Help()
	},
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&terraformFilePath, "file", "f", "", "The path to a terraform file.")
	rootCmd.PersistentFlags().StringVarP(&terraformDirectoryPath, "directory", "d", "", "The path to the root of a terraform repository.")
	rootCmd.MarkFlagsMutuallyExclusive("file", "directory")

	rootCmd.Flags().BoolVarP(&versionFlag, "version", "v", false, "Print the version number of terralint.")
}

func validateArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unexpected arguments %q: give one file with -f or one directory with -d", args)
	}
	if terraformFilePath == "" && terraformDirectoryPath == "" {
		return errors.New("exactly one of the command flags should be set")
	}

	if terraformFilePath != "" {
		terraformPath, _ = utilities.AbsPath(terraformFilePath)
		fileInfo, err := os.Stat(terraformPath)

		if err != nil {
			return err
		}

		if fileInfo.IsDir() {
			return errors.New("expected a file path, received a directory path")
		}

		return nil
	}
	terraformPath, _ = utilities.AbsPath(terraformDirectoryPath)
	fileInfo, err := os.Stat(terraformPath)

	if err != nil {
		return err
	}

	if !fileInfo.IsDir() {
		return errors.New("expected a directory path, received a file path")
	}
	return nil
}
