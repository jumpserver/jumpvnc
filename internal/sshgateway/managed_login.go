package sshgateway

// Only the service's installation key may request the machine-owned credential.
// Password decrypts it locally for one authenticated console submission.
type ManagedLogin struct {
	Fingerprint string
	Valid       func() bool
	Password    func() ([]byte, error)
}

const ManagedAccount = "JumpVNCUser"
