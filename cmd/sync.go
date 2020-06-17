package cmd

import (
	"log"
	"time"

	db "github.com/hongshibao/go-sync-mongo/db"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	mgo "gopkg.in/mgo.v2"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Tails the source oplog and syncs to destination",
	Long:  ``,
	Run: func(cmd *cobra.Command, args []string) {
		srcConfig := db.Config{
			URI:              viper.GetString("src"),
			SSL:              viper.GetBool("src-ssl"),
			IgnoreApplyError: viper.GetBool("ignore-apply-error"),
			Creds: mgo.Credential{
				Username: viper.GetString("src-username"),
				Password: viper.GetString("src-password"),
			},
			Timeout: time.Duration(viper.GetInt("timeout")) * time.Second,
		}
		src, err := db.NewConnection(srcConfig)
		if err != nil {
			log.Fatalf("Error: new src connection - %v\n", err)
		}

		dstConfig := db.Config{
			URI: viper.GetString("dst"),
			SSL: viper.GetBool("dst-ssl"),
			Creds: mgo.Credential{
				Username: viper.GetString("dst-username"),
				Password: viper.GetString("dst-password"),
			},
			Timeout: time.Duration(viper.GetInt("timeout")) * time.Second,
		}
		dst, err := db.NewConnection(dstConfig)
		if err != nil {
			log.Fatalf("Error: new dst connection - %v\n", err)
		}

		// timestamp recorder
		var diskTsRecorder db.TimestampRecorder
		filepath := viper.GetString("timestamp-recorder-filepath")
		if filepath != "" {
			diskTsRecorder, err = db.NewDiskTimestampRecorder(filepath)
			if err != nil {
				log.Fatalf("Error: new disk timestamp recorder - %v\n", err)
			}
		}

		err = src.SyncOplog(dst, diskTsRecorder)
		if err != nil {
			log.Fatalf("Error: sync oplog - %v\n", err)
		}
	},
}

func init() {
	RootCmd.AddCommand(syncCmd)
	syncCmd.Flags().Int32("since", 0, "seconds since the Unix epoch")
	syncCmd.Flags().Int32("ordinal", 0, "incrementing ordinal for operations within a given second")
	syncCmd.Flags().Int32("timeout", 0, "timeout in seconds for db connections, default is 300s")
	syncCmd.Flags().Bool("ignore-apply-error", false, "ingore error of applying oplog (true)")
	syncCmd.Flags().String("timestamp-recorder-filepath", "", "filepath for timestamp record")
	viper.BindPFlag("since", syncCmd.Flags().Lookup("since"))
	viper.BindPFlag("ordinal", syncCmd.Flags().Lookup("ordinal"))
	viper.BindPFlag("timeout", syncCmd.Flags().Lookup("timeout"))
	viper.BindPFlag("ignore-apply-error", syncCmd.Flags().Lookup("ignore-apply-error"))
	viper.BindPFlag("timestamp-recorder-filepath", syncCmd.Flags().Lookup("timestamp-recorder-filepath"))
}
