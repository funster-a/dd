package fakepsp

import "html/template"

type pageData struct {
	Payment    Payment
	AmountText string
}

var pageTmpl = template.Must(template.New("pay").Parse(`<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Оплата — тестовый шлюз</title>
<style>
  body { font-family: system-ui, sans-serif; background: #f4f4f5; margin: 0; padding: 16px; color: #18181b; }
  .card { max-width: 420px; margin: 40px auto; background: #fff; border-radius: 12px; padding: 24px; box-shadow: 0 1px 3px rgba(0,0,0,.1); }
  .badge { display: inline-block; background: #fef3c7; color: #92400e; border-radius: 6px; padding: 4px 8px; font-size: 13px; }
  .amount { font-size: 32px; font-weight: 600; margin: 16px 0 4px; }
  .muted { color: #71717a; font-size: 14px; }
  button { width: 100%; padding: 12px; margin-top: 12px; border: 0; border-radius: 8px; font-size: 16px; cursor: pointer; }
  .pay { background: #16a34a; color: #fff; } .fail { background: #e4e4e7; } .twice { background: #fff; border: 1px dashed #a1a1aa; font-size: 14px; }
</style>
</head>
<body>
<div class="card">
  <span class="badge">Тестовый платёжный шлюз — деньги не списываются</span>
  <div class="amount">{{.AmountText}}</div>
  <div class="muted">{{.Payment.Description}}</div>
  <div class="muted">Платёж {{.Payment.ID}} · статус: {{.Payment.Status}}</div>
  {{if eq .Payment.Status "pending"}}
  <form method="post" action="/pay/{{.Payment.ID}}">
    <button class="pay" name="action" value="succeed">Оплатить</button>
    <button class="fail" name="action" value="fail">Отказать</button>
    <button class="twice" name="action" value="succeed_twice">Оплатить, уведомление придёт дважды</button>
  </form>
  <p class="muted">Карта не нужна: это эмулятор провайдера для разработки и демонстрации.</p>
  {{end}}
</div>
</body>
</html>
`))
