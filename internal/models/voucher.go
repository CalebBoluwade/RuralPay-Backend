package models

type Voucher struct {
	ID                     int      `json:"id"`
	VoucherCode            string   `json:"voucherCode"`
	VoucherDescription     string   `json:"voucherDescription"`
	VoucherDiscountAmount  int64    `json:"voucherDiscountAmount" validate:"oneof=FIXED PERCENT"`
	VoucherType            string   `json:"voucherType"`
	VoucherAllowedServices []string `json:"voucherAllowedServices"`
}
