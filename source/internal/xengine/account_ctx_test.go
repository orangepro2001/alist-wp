package xengine

import (
	"context"
	"errors"
	"testing"
)

func newCtxTestEngine(t *testing.T) (*pcsEngine, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := loadAccountStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := NewWithStore(store, defaultTransConfig())
	// 预置一个"活跃账号"
	a, err := e.AddAccountByBDUSS("seed-bduss", "seed-stoken")
	if err != nil {
		t.Fatal(err)
	}
	if e.activeUID != a.UID {
		t.Fatalf("seed 账号应为活跃账号")
	}
	return e, dir
}

func TestEnsureAccountDoesNotSwitchActive(t *testing.T) {
	e, dir := newCtxTestEngine(t)
	seed := e.activeUID

	uid, err := e.EnsureAccount("BDUSS=aaa111; STOKEN=s1")
	if err != nil || uid == 0 || uid == seed {
		t.Fatalf("EnsureAccount = %d, %v", uid, err)
	}
	if e.activeUID != seed {
		t.Error("EnsureAccount 不应改变全局活跃账号")
	}
	if got, _ := e.EnsureAccount("BDUSS=aaa111; STOKEN=s1"); got != uid {
		t.Errorf("同 Cookie 应得同 uid: %d vs %d", got, uid)
	}
	// 同 BDUSS、Cookie 变了(如刷新了 STOKEN) → 更新凭证, 丢弃旧 pcs 实例
	e.mu.Lock()
	e.pool[uid] = nil
	e.mu.Unlock()
	if got, _ := e.EnsureAccount("BDUSS=aaa111; STOKEN=s2"); got != uid {
		t.Fatalf("同 BDUSS 应同 uid")
	}
	e.mu.RLock()
	_, cached := e.pool[uid]
	cookie := e.store.get(uid).Cookie
	e.mu.RUnlock()
	if cached || cookie != "BDUSS=aaa111; STOKEN=s2" {
		t.Errorf("Cookie 变化应更新凭证并清掉旧实例: cached=%v cookie=%q", cached, cookie)
	}
	if _, err := e.EnsureAccount("   "); err == nil {
		t.Error("空 Cookie 应报错")
	}
	// 已持久化: 重新加载 store 能看到该账号
	again, err := loadAccountStore(dir)
	if err != nil || again.get(uid) == nil {
		t.Errorf("账号应已落盘: %v", err)
	}
}

func TestActiveForPicksAccountFromContext(t *testing.T) {
	e, _ := newCtxTestEngine(t)
	other, _ := e.EnsureAccount("BDUSS=other-bduss; STOKEN=x")

	pcsActive, err := e.activeFor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pcsOther, err := e.activeFor(WithAccount(context.Background(), other))
	if err != nil {
		t.Fatal(err)
	}
	if pcsActive == pcsOther {
		t.Fatal("ctx 指定账号应得到不同于活跃账号的实例")
	}
	if again, _ := e.activeFor(WithAccount(context.Background(), other)); again != pcsOther {
		t.Error("同账号应复用实例")
	}
	_, err = e.activeFor(WithAccount(context.Background(), 12345))
	var xe *Error
	if !errors.As(err, &xe) || xe.Code != ErrAuth {
		t.Errorf("ctx 账号不存在应 ErrAuth, got %v", err)
	}
}

func TestCarryAccount(t *testing.T) {
	src := WithAccount(context.Background(), 7)
	dst := CarryAccount(src, context.Background())
	if uid, ok := AccountFrom(dst); !ok || uid != 7 {
		t.Errorf("CarryAccount 应带上 uid 7, got %d %v", uid, ok)
	}
	if _, ok := AccountFrom(CarryAccount(context.Background(), context.Background())); ok {
		t.Error("源 ctx 无账号时不应凭空出现")
	}
}
