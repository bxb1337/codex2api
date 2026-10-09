package auth

import (
	"log"
	"time"
)

type InferenceSessionBuffer struct {
	SessionKey string
	Guard      SessionAffinityGuard
}

// accountAdmissionLoad 保留真实占用统计，仅从调度压力中扣除可让位的缓冲。
func accountAdmissionLoad(acc *Account) int64 {
	if acc == nil {
		return 0
	}
	load := accountOccupiedRequests(acc) - acc.ReclaimableSlots.Load()
	if active := acc.GetActiveRequests(); load < active {
		return active
	}
	return load
}

// BufferInferenceSession 终态已经释放真实槽位，成功提交后只在有余量时保留缓冲。
func (s *Store) BufferInferenceSession(acc *Account, options InferenceSessionBuffer) {
	buffer := s.GetSessionSlotBuffer()
	if acc == nil || options.SessionKey == "" || options.Guard.PreservesExisting() ||
		!s.SessionSlotBufferEnabled() || buffer <= 0 || s.GetAffinityMode() == AffinityModeOff {
		return
	}
	limit := acc.GetDynamicConcurrencyLimit()
	if limit <= 0 {
		_, _, _, limit = acc.schedulerSnapshot(s.maxConcurrency.Load())
	}
	s.sessionMu.Lock()
	if !s.SessionSlotBufferEnabled() || !reserveBufferedAccountSlot(acc, limit) {
		s.sessionMu.Unlock()
		return
	}
	if s.sessionSlotReservations == nil {
		s.sessionSlotReservations = make(map[int64]map[string][]uint64)
	}
	if s.reclaimableSessionSlots == nil {
		s.reclaimableSessionSlots = make(map[uint64]bool)
	}
	s.sessionSlotSequence++
	id := s.sessionSlotSequence
	bySession := s.sessionSlotReservations[acc.DBID]
	if bySession == nil {
		bySession = make(map[string][]uint64)
		s.sessionSlotReservations[acc.DBID] = bySession
	}
	bySession[options.SessionKey] = append(bySession[options.SessionKey], id)
	s.reclaimableSessionSlots[id] = true
	acc.ReclaimableSlots.Add(1)
	s.sessionMu.Unlock()
	time.AfterFunc(buffer, func() { s.expireSessionSlot(acc, options.SessionKey, id) })
	s.notifySchedulerAccountAvailability(acc, true)
}

func reserveBufferedAccountSlot(acc *Account, limit int64) bool {
	for !accountDispatchBlocked(acc) && limit > 0 {
		occupied := acc.OccupiedRequests.Load()
		if occupied >= limit {
			return false
		}
		if acc.OccupiedRequests.CompareAndSwap(occupied, occupied+1) {
			return true
		}
	}
	return false
}

// 调用方持有 sessionMu；reservation ID 防止旧定时器重复释放。
func (s *Store) removeReclaimableSlotLocked(acc *Account, id uint64) {
	if s.reclaimableSessionSlots[id] {
		delete(s.reclaimableSessionSlots, id)
		atomicDecrementIfPositive(&acc.ReclaimableSlots)
	}
}

func (s *Store) reclaimBufferedCapacity(acc *Account, limit int64) {
	if acc.ReclaimableSlots.Load() == 0 || accountOccupiedRequests(acc) < limit {
		return
	}
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	for accountOccupiedRequests(acc) >= limit && acc.GetActiveRequests() < limit {
		if !s.reclaimOldestBufferedSlotLocked(acc) {
			return
		}
	}
}

func (s *Store) reclaimOldestBufferedSlotLocked(acc *Account) bool {
	var oldest uint64
	var owner string
	bySession := s.sessionSlotReservations[acc.DBID]
	for key, ids := range bySession {
		for _, id := range ids {
			if s.reclaimableSessionSlots[id] && (oldest == 0 || id < oldest) {
				oldest, owner = id, key
			}
		}
	}
	if oldest == 0 {
		return false
	}
	ids := bySession[owner]
	for i, id := range ids {
		if id == oldest {
			bySession[owner] = append(ids[:i], ids[i+1:]...)
			break
		}
	}
	if len(bySession[owner]) == 0 {
		delete(bySession, owner)
	}
	if len(bySession) == 0 {
		delete(s.sessionSlotReservations, acc.DBID)
	}
	s.removeReclaimableSlotLocked(acc, oldest)
	atomicDecrementIfPositive(&acc.OccupiedRequests)
	log.Printf("推理会话缓冲让位 account=%d reservation=%d", acc.DBID, oldest)
	return true
}

func (account *Account) GetReclaimableSlots() int64 {
	return account.ReclaimableSlots.Load()
}
