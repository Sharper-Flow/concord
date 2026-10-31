package store

import "context"

// LawAdditionReservation is one folded reservation of a law id: the earliest
// approved contract that added the id holds it for its Product until the
// reservation is released. The reservation is what a CD-id allocator must
// treat as taken before the record lands on the default branch.
type LawAdditionReservation struct {
	LawID                string `json:"law_id"`
	OwnerWorkID          string `json:"owner_work_id"`
	OwnerContractVersion int64  `json:"owner_contract_version"`
	HomeDomainID         string `json:"home_domain_id"`
}

// LawAdditionReservations lists the Product's law-addition reservations in
// law-id order. The read is unauthenticated for the same reason
// project-resolve is: the trust boundary is filesystem access to the
// authority database, and the read appends no event.
func (s *Store) LawAdditionReservations(ctx context.Context, productID string) ([]LawAdditionReservation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT law_id,owner_work_id,owner_contract_version,home_domain_id FROM workflow_law_addition_reservations WHERE product_id=? ORDER BY law_id`, productID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "cd_reservations", "cannot read the law-addition reservations", true, "retry once the reservation projection is readable", err)
	}
	defer rows.Close()
	out := []LawAdditionReservation{}
	for rows.Next() {
		var reservation LawAdditionReservation
		if err := rows.Scan(&reservation.LawID, &reservation.OwnerWorkID, &reservation.OwnerContractVersion, &reservation.HomeDomainID); err != nil {
			return nil, wrapFailure(KindUnavailable, "cd_reservations", "cannot decode a law-addition reservation", true, "retry once the reservation projection is readable", err)
		}
		out = append(out, reservation)
	}
	return out, rows.Err()
}
