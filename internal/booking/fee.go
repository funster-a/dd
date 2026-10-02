package booking

// Сервисный сбор с покупателя (ADR 019). Ставка — в базисных пунктах
// (сотых долях процента), чтобы обойтись без float: 500 = 5 %.

// WithServiceFee задаёт ставку сервисного сбора с покупателя. Без опции
// сбора нет: так ведут себя тесты и инструменты нагрузочного стенда.
func WithServiceFee(bps int32) Option { return func(s *Service) { s.feeBps = bps } }

// ServiceFeeBps — ставка сервисного сбора этого сервиса.
func (s *Service) ServiceFeeBps() int32 { return s.feeBps }

// ServiceFee — сбор с билета ценой price тиынов при ставке bps, с
// округлением до тиына половиной вверх. Бесплатный билет сбора не несёт.
func ServiceFee(price int64, bps int32) int64 {
	if price <= 0 || bps <= 0 {
		return 0
	}
	return (price*int64(bps) + 5000) / 10000
}
