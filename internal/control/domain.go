package control

type Domain string

const (
	DomainSystem     Domain = "system"
	DomainQueue      Domain = "queue"
	DomainProject    Domain = "project"
	DomainSession    Domain = "thread"
	DomainPreference Domain = "preference"
	DomainDelivery   Domain = "delivery"
	DomainSecurity   Domain = "security"
)

func (domain Domain) Valid() bool {
	switch domain {
	case DomainSystem, DomainQueue, DomainProject, DomainSession, DomainPreference, DomainDelivery, DomainSecurity:
		return true
	default:
		return false
	}
}
