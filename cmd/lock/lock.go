package lock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"statectl/internal/aws/lock"
	"statectl/internal/aws/utils"
	"statectl/internal/config"
	"statectl/internal/utils/subproc"
	t "statectl/internal/utils/types"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	bucket string
	key    string
)

func init() {
	AcquireCmd.Flags().StringVarP(&bucket, "bucket", "b", viper.GetString("BUCKET_NAME"), "S3 bucket to store the lock file")
	AcquireCmd.Flags().StringVarP(&key, "key", "k", viper.GetString("LOCK_KEY_PATH"), "S3 key to store the lock file")

	ReleaseCmd.Flags().StringVarP(&bucket, "bucket", "b", viper.GetString("BUCKET_NAME"), "S3 bucket to store the lock file")
	ReleaseCmd.Flags().StringVarP(&key, "key", "k", viper.GetString("LOCK_KEY_PATH"), "S3 key to store the lock file")

	ForceReleaseCmd.Flags().StringVarP(&bucket, "bucket", "b", viper.GetString("BUCKET_NAME"), "S3 bucket to store the lock file")
	ForceReleaseCmd.Flags().StringVarP(&key, "key", "k", viper.GetString("LOCK_KEY_PATH"), "S3 key to store the lock file")
}

var AcquireCmd = &cobra.Command{
	Use:   "acquire",
	Short: "Acquire a lock on the S3 bucket",
	Long: `Acquire a lock on the S3 bucket to prevent concurrent state modifications.
This command attempts to create a lock file in the specified S3 bucket, which
signals to other users and processes that the state file is currently being
modified. If the lock is already present, the command will fail and indicate
that the state file is in use.

Usage:
  statectl lock acquire

Example:
  # Acquire a lock on the S3 state file
  statectl lock acquire`,
	PreRun: func(cmd *cobra.Command, args []string) {
		if verbose, _ := cmd.Flags().GetBool("verbose"); verbose {
			log.SetLevel(logrus.DebugLevel)
		}
		log.Debug("Running lock acquire command")
	},
	Run: func(cmd *cobra.Command, args []string) {
		extra_comment := ""

		bucket, key, err := utils.GetS3BucketAndKey(cmd)
		if err != nil {
			cmd.PrintErrln(config.Red("❌ Failed to get S3 bucket and key: ", err))
			os.Exit(1)
		}
		log.Debug("S3 bucket/key: ", bucket, key)

		commit_sha := os.Getenv("CI_COMMIT_SHA")
		cs_comment := "ok"
		if commit_sha == "" {
			commit_sha, err = subproc.FetchLocalSHA()
			cs_comment = "No CI commit SHA available, using local commit SHA"
			if err != nil {
				commit_sha = uuid.New().String()
				cs_comment = "No commit SHA available, using random UUID"
			}
		}

		trigger_iid := os.Getenv("CI_PIPELINE_IID")
		ti_comment := "ok"
		if trigger_iid == "" {
			trigger_iid = uuid.New().String()
			ti_comment = "No pipeline ID available, using random UUID"
		}

		if cs_comment != "ok" || ti_comment != "ok" {
			extra_comment = "WARNING: one or more environment variables were not found. Use timestamp as reference to check the exact commit and pipeline ID."
		}

		lockInfo := t.LockInfo{
			LockID:    commit_sha,
			TimeStamp: time.Now().Format(time.RFC3339),
			Signer:    trigger_iid,
			Comments: t.Comments{
				Commit:  cs_comment,
				Trigger: ti_comment,
				Extra:   extra_comment,
			},
		}

		cli := utils.GetS3Client()

		if err := lock.AcquireStateLock(context.Background(), cli, bucket, key, lockInfo); err != nil {
			if errors.Is(err, lock.ErrLockExists) {
				cmd.Println(config.Yellow("Lock already acquired, exiting..."))
				os.Exit(0)
			}
			cmd.PrintErrln(config.Red("❌ Failed to acquire lock: ", err))
			os.Exit(1)
		}

		cmd.Println(config.Green("Lock acquired successfully."))
	},
}

var ReleaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Release the lock on the S3 bucket",
	Long: `Release the lock on the S3 bucket to allow other modifications.
This command removes the lock file from the S3 bucket, indicating that
the state file is no longer being modified and is available for other
users and processes to modify.

Usage:
  statectl lock release

Example:
  # Release the lock on the S3 state file
  statectl lock release`,
	PreRun: func(cmd *cobra.Command, args []string) {
		if verbose, _ := cmd.Flags().GetBool("verbose"); verbose {
			log.SetLevel(logrus.DebugLevel)
		}
		log.Debug("Running lock release command")
	},
	Run: func(cmd *cobra.Command, args []string) {
		bucket, key, err := utils.GetS3BucketAndKey(cmd)
		if err != nil {
			cmd.PrintErrln(config.Red("❌ Failed to get S3 bucket and key: ", err))
			os.Exit(1)
		}
		log.Debug("S3 bucket/key: ", bucket, key)

		cli := utils.GetS3Client()

		if err := lock.ReleaseStateLock(context.Background(), cli, bucket, key); err != nil {
			cmd.PrintErrln(config.Red("❌ Failed to release lock: ", err))
			os.Exit(1)
		}
		fmt.Println(config.Green("Lock released successfully."))
	},
}

var ForceReleaseCmd = &cobra.Command{
	Use:   "force-release",
	Short: "Force release the S3 lock with confirmation",
	Long: `Forcefully releases the lock on the S3 state file after user confirmation.
This command should be used with caution as it can disrupt ongoing operations.
It synchronizes the local state with the latest state from the S3 bucket.

Usage:
  statectl lock force-release

Example:
  # Prompt for confirmation and then force release the S3 lock
  statectl lock force-release`,
	PreRun: func(cmd *cobra.Command, args []string) {
		if verbose, _ := cmd.Flags().GetBool("verbose"); verbose {
			log.SetLevel(logrus.DebugLevel)
		}
		log.Debug("Preparing to prompt for lock force-release")
	},
	Run: func(cmd *cobra.Command, args []string) {
		bucket, key, err := utils.GetS3BucketAndKey(cmd)
		if err != nil {
			cmd.PrintErrln(config.Red("Failed to get S3 bucket and key: ", err))
			os.Exit(1)
		}
		log.Debug("S3 bucket/key: ", bucket, key)

		cli := utils.GetS3Client()

		if exist, _, err := lock.CheckStateLock(context.Background(), cli, bucket, key, false); err != nil {
			cmd.PrintErrln(config.Red("❌ Failed to check lock status: ", err))
			os.Exit(1)
		} else if !exist {
			cmd.PrintErrln(config.Red("❌ Lock does not exist. Nothing to release."))
			os.Exit(1)
		}

		reader := bufio.NewReader(os.Stdin)

		cmd.Println(config.Yellow("WARNING: You are about to forcefully remove the remote lock file. This may disrupt ongoing operations."))
		cmd.Println(config.Cyan("Are you sure you want to proceed? (type 'yes' to confirm): "))

		confirmation, _ := reader.ReadString('\n')
		if strings.TrimSpace(confirmation) != "yes" {
			cmd.Println(config.Yellow("Force release cancelled."))
			return
		}

		// User confirmed, proceed with force release
		err = lock.ForceReleaseLock(context.Background(), cli, bucket, key)
		if err != nil {
			cmd.PrintErrln(config.Red("Failed to force release lock: ", err))
			os.Exit(1)
		}
		fmt.Println(config.Green("Lock forcefully released successfully."))
	},
}
