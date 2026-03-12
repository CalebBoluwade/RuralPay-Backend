package utils

func MaskPAN(pan string) string {
	if len(pan) < 10 {
		return "****"
	}
	return pan[:6] + "****" + pan[len(pan)-4:]
}

func MaskPhone(phone string) string {
	if len(phone) < 8 {
		return "***"
	}
	return phone[:3] + "***" + phone[len(phone)-2:]
}

func MaskEmail(email string) string {
	if len(email) < 5 {
		return "**"
	}
	i := 0
	for ; i < len(email); i++ {
		if email[i] == '@' {
			break
		}
	}
	if i == len(email) {
		return "**"
	}
	return email[:2] + "***" + email[i-2:]
}

func MaskAccountNumber(accountNumber string) string {
	return accountNumber[:4] + "****" + accountNumber[len(accountNumber)-4:]
}
