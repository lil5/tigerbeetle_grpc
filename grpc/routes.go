package grpc

import (
	"context"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/lil5/tigerbeetle_api/proto"
	"github.com/lil5/tigerbeetle_api/shared"

	"github.com/charithe/timedbuf/v2"
	"github.com/samber/lo"
	tb "github.com/tigerbeetle/tigerbeetle-go"
	"github.com/tigerbeetle/tigerbeetle-go/pkg/types"
)

var (
	ErrZeroAccounts  = errors.New("no accounts were specified")
	ErrZeroTransfers = errors.New("no transfers were specified")
)

type TimedPayloadResponse struct {
	Results []types.TransferEventResult
	Error   error
}
type TimedPayload struct {
	c       chan TimedPayloadResponse
	payload []types.Transfer
}

type App struct {
	proto.UnimplementedTigerBeetleServer
	TB       tb.Client
	TimedBuf *timedbuf.TimedBuf[TimedPayload]
}

func NewApp(tb tb.Client) *App {
	app := &App{TB: tb}
	if os.Getenv("IS_BUFFERED") == "true" {
		bufSize, _ := strconv.Atoi(os.Getenv("BUFFER_SIZE"))
		if bufSize == 0 {
			bufSize = 1024
		}
		bufDelay, err := time.ParseDuration(os.Getenv("BUFFER_DELAY"))
		if err != nil {
			bufDelay = 100 * time.Millisecond
		}
		app.TimedBuf = timedbuf.New(bufSize, bufDelay, func(payloads []TimedPayload) {
			// Collect all transfers into one big array
			transfers := []types.Transfer{}
			for _, p := range payloads {
				transfers = append(transfers, p.payload...)
			}

			results, err := app.TB.CreateTransfers(transfers)

			res := TimedPayloadResponse{
				Results: results,
				Error:   err,
			}
			for _, p := range payloads {
				p.c <- res
			}
		})
	}
	return app
}

func (s *App) GetID(ctx context.Context, in *proto.GetIDRequest) (*proto.GetIDReply, error) {
	return &proto.GetIDReply{Id: types.ID().String()}, nil
}

func (s *App) CreateAccounts(ctx context.Context, in *proto.CreateAccountsRequest) (*proto.CreateAccountsReply, error) {
	if len(in.Accounts) == 0 {
		return nil, ErrZeroAccounts
	}
	accounts := []types.Account{}
	for _, inAccount := range in.Accounts {
		id, err := shared.HexStringToUint128(inAccount.Id)
		if err != nil {
			return nil, err
		}
		userData128, err := types.HexStringToUint128(inAccount.UserData128)
		if err != nil {
			return nil, err
		}
		flags := types.AccountFlags{
			Linked:                     lo.FromPtrOr(inAccount.Flags.Linked, false),
			DebitsMustNotExceedCredits: lo.FromPtrOr(inAccount.Flags.DebitsMustNotExceedCredits, false),
			CreditsMustNotExceedDebits: lo.FromPtrOr(inAccount.Flags.CreditsMustNotExceedDebits, false),
			History:                    lo.FromPtrOr(inAccount.Flags.History, false),
		}
		accounts = append(accounts, types.Account{
			ID:             *id,
			DebitsPending:  types.ToUint128(uint64(inAccount.DebitsPending)),
			DebitsPosted:   types.ToUint128(uint64(inAccount.DebitsPosted)),
			CreditsPending: types.ToUint128(uint64(inAccount.CreditsPending)),
			CreditsPosted:  types.ToUint128(uint64(inAccount.CreditsPosted)),
			UserData128:    userData128,
			UserData64:     uint64(inAccount.UserData64),
			UserData32:     uint32(inAccount.UserData32),
			Ledger:         uint32(inAccount.Ledger),
			Code:           uint16(inAccount.Code),
			Flags:          flags.ToUint16(),
		})
	}

	resp, err := s.TB.CreateAccounts(accounts)
	if err != nil {
		return nil, err
	}

	resArr := []string{}
	for _, r := range resp {
		resArr = append(resArr, r.Result.String())
	}
	return &proto.CreateAccountsReply{
		Results: resArr,
	}, nil
}

func (s *App) CreateTransfers(ctx context.Context, in *proto.CreateTransfersRequest) (*proto.CreateTransfersReply, error) {
	if len(in.Transfers) == 0 {
		return nil, ErrZeroTransfers
	}
	transfers := []types.Transfer{}
	for _, inTransfer := range in.Transfers {
		id, err := shared.HexStringToUint128(inTransfer.Id)
		if err != nil {
			return nil, err
		}
		flags := types.TransferFlags{
			Linked:              lo.FromPtrOr(inTransfer.TransferFlags.Linked, false),
			Pending:             lo.FromPtrOr(inTransfer.TransferFlags.Pending, false),
			PostPendingTransfer: lo.FromPtrOr(inTransfer.TransferFlags.PostPendingTransfer, false),
			VoidPendingTransfer: lo.FromPtrOr(inTransfer.TransferFlags.VoidPendingTransfer, false),
			BalancingDebit:      lo.FromPtrOr(inTransfer.TransferFlags.BalancingDebit, false),
			BalancingCredit:     lo.FromPtrOr(inTransfer.TransferFlags.BalancingCredit, false),
		}
		debitAccountID, err := shared.HexStringToUint128(inTransfer.DebitAccountId)
		if err != nil {
			return nil, err
		}
		creditAccountID, err := shared.HexStringToUint128(inTransfer.CreditAccountId)
		if err != nil {
			return nil, err
		}
		pendingID, err := shared.HexStringToUint128(lo.FromPtrOr(inTransfer.PendingId, ""))
		if err != nil {
			return nil, err
		}
		userData128, err := types.HexStringToUint128(inTransfer.UserData128)
		if err != nil {
			return nil, err
		}
		transfers = append(transfers, types.Transfer{
			ID:              *id,
			DebitAccountID:  *debitAccountID,
			CreditAccountID: *creditAccountID,
			Amount:          types.ToUint128(uint64(inTransfer.Amount)),
			PendingID:       *pendingID,
			UserData128:     userData128,
			UserData64:      uint64(inTransfer.UserData64),
			UserData32:      uint32(inTransfer.UserData32),
			Timeout:         0,
			Ledger:          uint32(inTransfer.Ledger),
			Code:            uint16(inTransfer.Ledger),
			Flags:           flags.ToUint16(),
			Timestamp:       0,
		})
	}

	var results []types.TransferEventResult
	var err error
	if s.TimedBuf != nil {
		c := make(chan TimedPayloadResponse)
		s.TimedBuf.Put(TimedPayload{
			c:       c,
			payload: transfers,
		})
		resp := <-c
		err = resp.Error
		results = resp.Results
	} else {
		results, err = s.TB.CreateTransfers(transfers)
	}
	if err != nil {
		return nil, err
	}
	resArr := []string{}
	for _, r := range results {
		resArr = append(resArr, r.Result.String())
	}
	return &proto.CreateTransfersReply{
		Results: resArr,
	}, nil
}

func (s *App) LookupAccounts(ctx context.Context, in *proto.LookupAccountsRequest) (*proto.LookupAccountsReply, error) {
	if len(in.AccountIds) == 0 {
		return nil, ErrZeroAccounts
	}
	ids := []types.Uint128{}
	for _, inID := range in.AccountIds {
		id, err := shared.HexStringToUint128(inID)
		if err != nil {
			return nil, err
		}
		ids = append(ids, *id)
	}

	res, err := s.TB.LookupAccounts(ids)
	if err != nil {
		return nil, err
	}

	pAccounts := lo.Map(res, func(a types.Account, _ int) *proto.Account {
		return AccountToProtoAccount(a)
	})
	return &proto.LookupAccountsReply{Accounts: pAccounts}, nil
}

func (s *App) LookupTransfers(ctx context.Context, in *proto.LookupTransfersRequest) (*proto.LookupTransfersReply, error) {
	if len(in.TransferIds) == 0 {
		return nil, ErrZeroTransfers
	}
	ids := []types.Uint128{}
	for _, inID := range in.TransferIds {
		id, err := shared.HexStringToUint128(inID)
		if err != nil {
			return nil, err
		}
		ids = append(ids, *id)
	}

	res, err := s.TB.LookupTransfers(ids)
	if err != nil {
		return nil, err
	}

	pTransfers := lo.Map(res, func(a types.Transfer, _ int) *proto.Transfer {
		return TransferToProtoTransfer(a)
	})
	return &proto.LookupTransfersReply{Transfers: pTransfers}, nil
}

func (s *App) GetAccountTransfers(ctx context.Context, in *proto.GetAccountTransfersRequest) (*proto.GetAccountTransfersReply, error) {
	if in.Filter.AccountId == "" {
		return nil, ErrZeroAccounts
	}
	tbFilter, err := AccountFilterFromProtoToTigerbeetle(in.Filter)
	if err != nil {
		return nil, err
	}
	res, err := s.TB.GetAccountTransfers(*tbFilter)
	if err != nil {
		return nil, err
	}

	pTransfers := lo.Map(res, func(v types.Transfer, _ int) *proto.Transfer {
		return TransferToProtoTransfer(v)
	})
	return &proto.GetAccountTransfersReply{Transfers: pTransfers}, nil
}

func (s *App) GetAccountBalances(ctx context.Context, in *proto.GetAccountBalancesRequest) (*proto.GetAccountBalancesReply, error) {
	if in.Filter.AccountId == "" {
		return nil, ErrZeroAccounts
	}
	tbFilter, err := AccountFilterFromProtoToTigerbeetle(in.Filter)
	if err != nil {
		return nil, err
	}
	res, err := s.TB.GetAccountBalances(*tbFilter)
	if err != nil {
		return nil, err
	}

	pBalances := lo.Map(res, func(v types.AccountBalance, _ int) *proto.AccountBalance {
		return AccountBalanceFromTigerbeetleToProto(v)
	})
	return &proto.GetAccountBalancesReply{AccountBalances: pBalances}, nil
}
