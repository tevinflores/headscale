package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	v1 "github.com/juanfont/headscale/gen/go/headscale/v1"
	"github.com/juanfont/headscale/hscontrol/util"
	"github.com/juanfont/headscale/hscontrol/util/zlog/zf"
	"github.com/pterm/pterm"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// CLI user errors.
var (
	errFlagRequired       = errors.New("--username or --identifier flag is required")
	errMultipleUsersMatch = errors.New("multiple users match query, specify an ID")
)



func init() {
	rootCmd.AddCommand(userCmd)
	userCmd.AddCommand(createUserCmd)
	createUserCmd.Flags().StringP("display-name", "d", "", "Display name")
	createUserCmd.Flags().StringP("email", "e", "", "Email")
	createUserCmd.Flags().StringP("picture-url", "p", "", "Profile picture URL")
	userCmd.AddCommand(listUsersCmd)
	usernameAndIDFlag(listUsersCmd)
	listUsersCmd.Flags().StringP("email", "e", "", "email")
	userCmd.AddCommand(destroyUserCmd)
	usernameAndIDFlag(destroyUserCmd)
	userCmd.AddCommand(renameUserCmd)
	renameUserCmd.Flags().Int64P("identifier", "i", -1, "User identifier (ID)")
}

var userCmd = &cobra.Command{
	Use:     "users",
	Short:   "Manage the users of Headscale",
	Aliases: []string{"user"},
}

var createUserCmd = &cobra.Command{
	Use:     "create NAME",
	Short:   "Creates a new user",
	Aliases: []string{"c", "new"},
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 {
			return errMissingParameter
		}

		return nil
	},
	RunE: grpcRunE(func(ctx context.Context, client v1.HeadscaleServiceClient, cmd *cobra.Command, args []string) error {
		userName := args[0]

		log.Trace().Interface(zf.Client, client).Msg("obtained gRPC client")

		request := &v1.CreateUserRequest{Name: userName}

		if displayName, _ := cmd.Flags().GetString("display-name"); displayName != "" {
			request.DisplayName = displayName
		}

		if email, _ := cmd.Flags().GetString("email"); email != "" {
			request.Email = email
		}

		if pictureURL, _ := cmd.Flags().GetString("picture-url"); pictureURL != "" {
			if _, err := url.Parse(pictureURL); err != nil { //nolint:noinlineerr
				return fmt.Errorf("invalid picture URL: %w", err)
			}

			request.PictureUrl = pictureURL
		}

		log.Trace().Interface(zf.Request, request).Msg("sending CreateUser request")

		response, err := client.CreateUser(ctx, request)
		if err != nil {
			return fmt.Errorf("creating user: %w", err)
		}

		return printOutput(cmd, response.GetUser(), "User created")
	}),
}

var destroyUserCmd = &cobra.Command{
	Use:     "destroy --identifier ID or --username USERNAME",
	Short:   "Destroys a user",
	Aliases: []string{"delete"},
	RunE: grpcRunE(func(ctx context.Context, client v1.HeadscaleServiceClient, cmd *cobra.Command, args []string) error {
		id, username, err := usernameAndIDFromFlag(cmd)
		if err != nil {
			return err
		}

		request := &v1.ListUsersRequest{
			Name: username,
			Id:   id,
		}

		users, err := client.ListUsers(ctx, request)
		if err != nil {
			return fmt.Errorf("listing users: %w", err)
		}

		if len(users.GetUsers()) != 1 {
			return errMultipleUsersMatch
		}

		user := users.GetUsers()[0]

		if !confirmAction(cmd, fmt.Sprintf(
			"Do you want to remove the user %q (%d) and any associated preauthkeys?",
			user.GetName(), user.GetId(),
		)) {
			return printOutput(cmd, map[string]string{"Result": "User not destroyed"}, "User not destroyed")
		}

		deleteRequest := &v1.DeleteUserRequest{Id: user.GetId()}

		response, err := client.DeleteUser(ctx, deleteRequest)
		if err != nil {
			return fmt.Errorf("destroying user: %w", err)
		}

		return printOutput(cmd, response, "User destroyed")
	}),
}

var listUsersCmd = &cobra.Command{
	Use:     "list",
	Short:   "List all the users",
	Aliases: []string{"ls", "show"},
	RunE: grpcRunE(func(ctx context.Context, client v1.HeadscaleServiceClient, cmd *cobra.Command, args []string) error {
		request := &v1.ListUsersRequest{}

		id, _ := cmd.Flags().GetInt64("identifier")
		username, _ := cmd.Flags().GetString("name")
		email, _ := cmd.Flags().GetString("email")

		// filter by one param at most
		switch {
		case id > 0:
			request.Id = uint64(id)
		case username != "":
			request.Name = username
		case email != "":
			request.Email = email
		}

		response, err := client.ListUsers(ctx, request)
		if err != nil {
			return fmt.Errorf("listing users: %w", err)
		}

		return printListOutput(cmd, response.GetUsers(), func() error {
			tableData := pterm.TableData{{"ID", "Name", "Username", "Email", "Created"}}
			for _, user := range response.GetUsers() {
				tableData = append(
					tableData,
					[]string{
						strconv.FormatUint(user.GetId(), util.Base10),
						user.GetDisplayName(),
						user.GetName(),
						user.GetEmail(),
						user.GetCreatedAt().AsTime().Format(HeadscaleDateTimeFormat),
					},
				)
			}

			return pterm.DefaultTable.WithHasHeader().WithData(tableData).Render()
		})
	}),
}

var renameUserCmd = &cobra.Command{
	Use:     "rename OLD_USERNAME NEW_USERNAME or rename -i ID NEW_USERNAME",
	Short:   "Renames a user",
	Aliases: []string{"mv"},
	Args: func(cmd *cobra.Command, args []string) error {
		identifier, _ := cmd.Flags().GetInt64("identifier")

		// Either -i flag with one arg, or two positional args
		if identifier >= 0 {
			// Using -i flag, need exactly one arg (new name)
			if len(args) != 1 {
				return fmt.Errorf("expected NEW_USERNAME argument when using --identifier flag")
			}
		} else {
			// Not using -i flag, need exactly two args (old name and new name)
			if len(args) != 2 {
				return fmt.Errorf("expected OLD_USERNAME and NEW_USERNAME as arguments")
			}
		}

		return nil
	},
	RunE: grpcRunE(func(ctx context.Context, client v1.HeadscaleServiceClient, cmd *cobra.Command, args []string) error {
		var id uint64
		var oldName string
		var newName string

		identifier, _ := cmd.Flags().GetInt64("identifier")

		if identifier >= 0 {
			id = uint64(identifier)
			newName = args[0]
		} else {
			oldName = args[0]
			newName = args[1]
		}

		listReq := &v1.ListUsersRequest{
			Name: oldName,
			Id:   id,
		}

		users, err := client.ListUsers(ctx, listReq)
		if err != nil {
			return fmt.Errorf("listing users: %w", err)
		}

		if len(users.GetUsers()) != 1 {
			return errMultipleUsersMatch
		}

		user := users.GetUsers()[0]

		renameReq := &v1.RenameUserRequest{
			OldId:   user.GetId(),
			NewName: newName,
		}

		response, err := client.RenameUser(ctx, renameReq)
		if err != nil {
			return fmt.Errorf("renaming user: %w", err)
		}

		return printOutput(cmd, response.GetUser(), "User renamed")
	}),
}
