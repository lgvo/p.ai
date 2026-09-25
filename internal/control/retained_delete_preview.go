package control

type RetainedDeletePreview struct {
	Project           string             `json:"project"`
	Branch            string             `json:"branch"`
	Ref               string             `json:"ref"`
	Tip               string             `json:"tip"`
	BranchLoss        *RemovalBranchLoss `json:"branch_loss"`
	ConfirmationToken string             `json:"confirmation_token"`
	ExpiresAt         string             `json:"expires_at"`
}
