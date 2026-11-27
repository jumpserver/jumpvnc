// SPDX-License-Identifier: GPL-2.0-or-later
#include "console_login.h"
#include <commctrl.h>
#include <sddl.h>
#include <shlwapi.h>
#include <shlguid.h>
#include <new>
#include <cwchar>

using namespace console_login;
extern "C" IMAGE_DOS_HEADER __ImageBase;

namespace {
LONG objects;
enum Field : DWORD { Title, Submit, Logo, Label, FieldCount };

// Keep only protocol milestones. No account names, SIDs, passwords or blobs
// belong in diagnostics from the secure desktop.
void trace(const wchar_t* stage, DWORD session, HRESULT status = S_OK) {
    HANDLE source = RegisterEventSourceW(nullptr, L"JumpVNC Credential Provider");
    if (!source) return;
    wchar_t message[160]{};
    swprintf(message, ARRAYSIZE(message), L"stage=%ls session=%lu status=0x%08lx", stage, session, static_cast<ULONG>(status));
    LPCWSTR fields[]{message};
    ReportEventW(source, FAILED(status) ? EVENTLOG_WARNING_TYPE : EVENTLOG_INFORMATION_TYPE, 0, 7690, nullptr, 1, 0, fields, nullptr);
    DeregisterEventSource(source);
}


class Counted {
    LONG refs_ = 1;
protected:
    Counted() { InterlockedIncrement(&objects); }
    virtual ~Counted() { InterlockedDecrement(&objects); }
    ULONG retain() { return InterlockedIncrement(&refs_); }
    ULONG release() {
        ULONG refs = InterlockedDecrement(&refs_);
        if (!refs) delete this;
        return refs;
    }
};

class Credential final : public ICredentialProviderCredential2, public Counted {
    HANDLE pipe_;
    Account account_;
    DWORD session_;
    ULONGLONG opened_;
    CREDENTIAL_PROVIDER_USAGE_SCENARIO scenario_;
    DWORD traced_ = 0;
    UserTile userTile_ = UserTile::Hidden;
    void close() {
        if (pipe_ != INVALID_HANDLE_VALUE) CloseHandle(pipe_);
        pipe_ = INVALID_HANDLE_VALUE;
    }
public:
    Credential(HANDLE pipe, const Account& account, DWORD session, ULONGLONG opened, CREDENTIAL_PROVIDER_USAGE_SCENARIO scenario)
        : pipe_(pipe), account_(account), session_(session), opened_(opened), scenario_(scenario) {}
    ~Credential() override { close(); }
    bool available() const {
        return pipe_ != INVALID_HANDLE_VALUE && isPhysicalConsole(session_) && GetTickCount64() - opened_ < requestLifetimeMs &&
               PeekNamedPipe(pipe_, nullptr, 0, nullptr, nullptr, nullptr);
    }
    void cancel() {
        userTile_ = UserTile::Hidden;
        close();
    }
    bool bindUsers(ICredentialProviderUserArray* users) {
        UserTile tile;
        HRESULT hr = selectUserTile(users, account_.sid, tile);
        if (tile == UserTile::Hidden) {
            trace(L"tile.binding.unavailable", session_, hr);
            cancel();
            return false;
        }
        if (userTile_ != tile)
            trace(tile == UserTile::EnumeratedUser ? L"tile.binding.enumerated_user" : L"tile.binding.other_local", session_);
        userTile_ = tile;
        return true;
    }
    enum Stage : DWORD { Count = 1, Enumerated = 2, Associated = 4, Selected = 8, Serialization = 16, Result = 32, Returned = 64 };
    void once(Stage flag, const wchar_t* stage, HRESULT status = S_OK) {
        if (traced_ & flag) return;
        traced_ |= flag;
        trace(stage, session_, status);
    }
    HRESULT STDMETHODCALLTYPE QueryInterface(REFIID iid, void** out) override {
        if (!out) return E_POINTER;
        *out = nullptr;
        if (iid != IID_IUnknown && iid != __uuidof(ICredentialProviderCredential) && iid != __uuidof(ICredentialProviderCredential2))
            return E_NOINTERFACE;
        *out = static_cast<ICredentialProviderCredential2*>(this);
        AddRef();
        return S_OK;
    }
    ULONG STDMETHODCALLTYPE AddRef() override { return retain(); }
    ULONG STDMETHODCALLTYPE Release() override { return release(); }
    HRESULT STDMETHODCALLTYPE Advise(ICredentialProviderCredentialEvents*) override { return S_OK; }
    HRESULT STDMETHODCALLTYPE UnAdvise() override { return S_OK; }
    HRESULT STDMETHODCALLTYPE SetSelected(BOOL* autologon) override {
        if (!autologon) return E_POINTER;
        *autologon = available();
        once(Selected, L"tile.selected", *autologon ? S_OK : S_FALSE);
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE SetDeselected() override { return S_OK; }
    HRESULT STDMETHODCALLTYPE GetUserSid(LPWSTR* value) override {
        if (!value) return E_POINTER;
        *value = nullptr;
        if (userTile_ == UserTile::Hidden) return E_ACCESSDENIED;
        if (userTile_ == UserTile::OtherLocal) {
            once(Associated, L"tile.other_local", S_FALSE);
            return S_FALSE;
        }
        LPWSTR sid;
        if (!ConvertSidToStringSidW(account_.sid, &sid)) return HRESULT_FROM_WIN32(GetLastError());
        HRESULT result = SHStrDupW(sid, value);
        once(Associated, L"tile.account_sid", result);
        LocalFree(sid);
        return result;
    }
    HRESULT STDMETHODCALLTYPE GetFieldState(DWORD id, CREDENTIAL_PROVIDER_FIELD_STATE* state,
                                            CREDENTIAL_PROVIDER_FIELD_INTERACTIVE_STATE* interactive) override {
        if (!state || !interactive || id >= FieldCount) return E_INVALIDARG;
        *state = CPFS_DISPLAY_IN_BOTH;
        if (id == Label) *state = CPFS_HIDDEN; // LogonUI owns the sign-in option tooltip.
        if (id == Submit) *state = CPFS_DISPLAY_IN_SELECTED_TILE;
        *interactive = CPFIS_NONE;
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE GetStringValue(DWORD id, LPWSTR* value) override {
        if (!value || (id != Title && id != Label)) return E_INVALIDARG;
        return SHStrDupW(id == Title ? L"JumpVNC" : L"JumpVNC Windows sign-in", value);
    }
    HRESULT STDMETHODCALLTYPE GetSubmitButtonValue(DWORD id, DWORD* adjacent) override {
        if (id != Submit || !adjacent) return E_INVALIDARG;
        *adjacent = Title;
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE GetSerialization(CREDENTIAL_PROVIDER_GET_SERIALIZATION_RESPONSE* response,
                                              CREDENTIAL_PROVIDER_CREDENTIAL_SERIALIZATION* serialized,
                                              LPWSTR* text, CREDENTIAL_PROVIDER_STATUS_ICON* icon) override {
        if (!response || !serialized || !text || !icon) return E_POINTER;
        *response = CPGSR_NO_CREDENTIAL_NOT_FINISHED;
        *serialized = {};
        *text = nullptr;
        *icon = CPSI_NONE;
        once(Serialization, L"tile.serialization_requested");
        HRESULT result = E_ACCESSDENIED;
        if (available() && canSignIn(session_, scenario_, account_))
            result = serialize(pipe_, account_, scenario_, serialized);
        close();
        once(Returned, L"tile.serialization_returned", result);
        if (SUCCEEDED(result)) *response = CPGSR_RETURN_CREDENTIAL_FINISHED;
        return result;
    }
    HRESULT STDMETHODCALLTYPE ReportResult(NTSTATUS status, NTSTATUS, LPWSTR* text, CREDENTIAL_PROVIDER_STATUS_ICON* icon) override {
        once(Result, L"tile.windows_result", status);
        if (text) *text = nullptr;
        if (icon) *icon = CPSI_NONE;
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE GetBitmapValue(DWORD id, HBITMAP* bitmap) override {
        if (!bitmap) return E_POINTER;
        *bitmap = nullptr;
        if (id != Logo) return E_INVALIDARG;
        *bitmap = static_cast<HBITMAP>(LoadImageW(reinterpret_cast<HINSTANCE>(&__ImageBase),
            MAKEINTRESOURCEW(101), IMAGE_BITMAP, 0, 0, LR_CREATEDIBSECTION));
        return *bitmap ? S_OK : HRESULT_FROM_WIN32(GetLastError());
    }
    HRESULT STDMETHODCALLTYPE GetCheckboxValue(DWORD, BOOL*, LPWSTR*) override { return E_NOTIMPL; }
    HRESULT STDMETHODCALLTYPE GetComboBoxValueCount(DWORD, DWORD*, DWORD*) override { return E_NOTIMPL; }
    HRESULT STDMETHODCALLTYPE GetComboBoxValueAt(DWORD, DWORD, LPWSTR*) override { return E_NOTIMPL; }
    HRESULT STDMETHODCALLTYPE SetStringValue(DWORD, LPCWSTR) override { return E_NOTIMPL; }
    HRESULT STDMETHODCALLTYPE SetCheckboxValue(DWORD, BOOL) override { return E_NOTIMPL; }
    HRESULT STDMETHODCALLTYPE SetComboBoxSelectedValue(DWORD, DWORD) override { return E_NOTIMPL; }
    HRESULT STDMETHODCALLTYPE CommandLinkClicked(DWORD) override { return E_NOTIMPL; }
};

class Provider final : public ICredentialProvider, public ICredentialProviderSetUserArray, public Counted {
    ICredentialProviderUserArray* users_ = nullptr;
    CREDENTIAL_PROVIDER_USAGE_SCENARIO scenario_ = CPUS_INVALID;
    DWORD session_ = 0;
    HANDLE invitation_ = INVALID_HANDLE_VALUE;
    ULONGLONG opened_ = 0;
    Credential* tile_ = nullptr;
    ICredentialProviderEvents* events_ = nullptr;
    UINT_PTR adviseContext_ = 0;
    HWND window_ = nullptr;
    bool firstTick_ = true;

    void closeInvitation() {
        if (invitation_ != INVALID_HANDLE_VALUE) CloseHandle(invitation_);
        invitation_ = INVALID_HANDLE_VALUE;
    }
    void clearTile() {
        if (tile_) { tile_->cancel(); tile_->Release(); }
        tile_ = nullptr;
    }
    bool refresh() {
        if (tile_) {
            if (tile_->available()) return false;
            clearTile();
            return true;
        }
        if (!users_ || !isPhysicalConsole(session_)) {
            closeInvitation();
            return false;
        }
        if (invitation_ == INVALID_HANDLE_VALUE) {
            invitation_ = openServicePipe();
            if (invitation_ == INVALID_HANDLE_VALUE) return false;
            opened_ = GetTickCount64();
            trace(scenario_ == CPUS_LOGON ? L"pipe.opened.logon" : L"pipe.opened.unlock", session_);
        }
        BYTE frame[headerBytes + maxUnits * sizeof(wchar_t)]{};
        DWORD available = 0, copied = 0;
        if (GetTickCount64() - opened_ >= requestLifetimeMs) {
            trace(L"pipe.header_expired", session_, HRESULT_FROM_WIN32(ERROR_TIMEOUT));
            closeInvitation();
            return false;
        }
        if (!PeekNamedPipe(invitation_, frame, sizeof(frame), &copied, &available, nullptr)) {
            trace(L"pipe.peek_failed", session_, HRESULT_FROM_WIN32(GetLastError()));
            closeInvitation();
            return false;
        }
        WORD units = 0;
        HeaderState state = readHeader(frame, copied, session_, units);
        if (state == HeaderState::Waiting) return false;
        if (state == HeaderState::Invalid || available != headerBytes + units * sizeof(wchar_t) ||
            !pipeTransfer(invitation_, frame, available, false)) {
            trace(L"pipe.invalid_header", session_, E_INVALIDARG);
            closeInvitation();
            return false;
        }
        wchar_t name[maxUnits + 1]{};
        memcpy(name, frame + headerBytes, units * sizeof(wchar_t));
        Account account{};
        if (!resolveLocalAccount(name, units, account)) {
            trace(L"tile.account_unavailable", session_, E_ACCESSDENIED);
            closeInvitation();
            return false;
        }
        if (!canSignIn(session_, scenario_, account)) {
            trace(L"tile.console_mismatch", session_, E_ACCESSDENIED);
            closeInvitation();
            return false;
        }
        tile_ = new (std::nothrow) Credential(invitation_, account, session_, opened_, scenario_);
        if (!tile_) { closeInvitation(); return false; }
        invitation_ = INVALID_HANDLE_VALUE;
        if (!tile_->bindUsers(users_)) {
            clearTile();
            return false;
        }
        trace(L"tile.ready", session_);
        return true;
    }
    // LogonUI's Apartment message pump keeps all COM callbacks on the advised
    // thread. The poll only peeks at public invitation metadata, never passwords.
    static LRESULT CALLBACK watch(HWND window, UINT message, WPARAM w, LPARAM l, UINT_PTR, DWORD_PTR data) {
        if (message != WM_TIMER || w != 1) return DefSubclassProc(window, message, w, l);
        auto* self = reinterpret_cast<Provider*>(data);
        self->AddRef();
        if (self->firstTick_) {
            self->firstTick_ = false;
            trace(L"provider.timer_started", self->session_);
        }
        if (self->refresh() && self->events_) {
            ICredentialProviderEvents* events = self->events_;
            events->AddRef();
            bool published = self->tile_ != nullptr;
            if (published) trace(L"tile.notifying", self->session_);
            HRESULT hr = events->CredentialsChanged(self->adviseContext_);
            if (published) trace(L"tile.notified", self->session_, hr);
            events->Release();
        }
        self->Release();
        return 0;
    }
public:
    ~Provider() override {
        UnAdvise();
        if (users_) users_->Release();
    }
    HRESULT STDMETHODCALLTYPE QueryInterface(REFIID iid, void** out) override {
        if (!out) return E_POINTER;
        *out = nullptr;
        if (iid == IID_IUnknown || iid == __uuidof(ICredentialProvider))
            *out = static_cast<ICredentialProvider*>(this);
        else if (iid == __uuidof(ICredentialProviderSetUserArray))
            *out = static_cast<ICredentialProviderSetUserArray*>(this);
        else
            return E_NOINTERFACE;
        AddRef();
        return S_OK;
    }
    ULONG STDMETHODCALLTYPE AddRef() override { return retain(); }
    ULONG STDMETHODCALLTYPE Release() override { return release(); }
    HRESULT STDMETHODCALLTYPE SetUsageScenario(CREDENTIAL_PROVIDER_USAGE_SCENARIO scenario, DWORD) override {
        clearTile();
        closeInvitation();
        if (users_) users_->Release();
        users_ = nullptr;
        scenario_ = CPUS_INVALID;
        session_ = 0;
        if (scenario != CPUS_LOGON && scenario != CPUS_UNLOCK_WORKSTATION) return E_NOTIMPL;
        if (!ProcessIdToSessionId(GetCurrentProcessId(), &session_) || !isPhysicalConsole(session_)) return E_NOTIMPL;
        scenario_ = scenario;
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE SetUserArray(ICredentialProviderUserArray* users) override {
        if (users) users->AddRef();
        if (users_) users_->Release();
        users_ = users;
        // Re-enumeration can replace the array while LogonUI holds an earlier
        // tile. Revoke its invitation if that account is no longer displayable.
        if (tile_ && !tile_->bindUsers(users_)) clearTile();
        if (!users_) closeInvitation();
        return users_ ? S_OK : E_INVALIDARG;
    }
    HRESULT STDMETHODCALLTYPE SetSerialization(const CREDENTIAL_PROVIDER_CREDENTIAL_SERIALIZATION*) override { return E_NOTIMPL; }
    HRESULT STDMETHODCALLTYPE Advise(ICredentialProviderEvents* events, UINT_PTR context) override {
        if (!events || scenario_ == CPUS_INVALID) return E_INVALIDARG;
        if (!window_) {
            window_ = CreateWindowExW(0, L"STATIC", nullptr, 0, 0, 0, 0, 0, HWND_MESSAGE, nullptr, nullptr, nullptr);
            if (!window_ || !SetWindowSubclass(window_, watch, 1, reinterpret_cast<DWORD_PTR>(this)) ||
                !SetTimer(window_, 1, 350, nullptr)) {
                UnAdvise();
                return E_FAIL;
            }
            firstTick_ = true;
        }
        trace(L"provider.advised", session_);
        events->AddRef();
        if (events_) events_->Release();
        events_ = events;
        adviseContext_ = context;
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE UnAdvise() override {
        if (window_) {
            KillTimer(window_, 1);
            RemoveWindowSubclass(window_, watch, 1);
            DestroyWindow(window_);
            window_ = nullptr;
        }
        if (events_) {
            auto* events = events_;
            events_ = nullptr;
            events->Release();
        }
        clearTile();
        closeInvitation();
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE GetFieldDescriptorCount(DWORD* count) override {
        if (!count) return E_POINTER;
        *count = FieldCount;
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE GetFieldDescriptorAt(DWORD index, CREDENTIAL_PROVIDER_FIELD_DESCRIPTOR** out) override {
        if (!out) return E_POINTER;
        *out = nullptr;
        if (index >= FieldCount) return E_INVALIDARG;
        auto* field = static_cast<CREDENTIAL_PROVIDER_FIELD_DESCRIPTOR*>(CoTaskMemAlloc(sizeof(CREDENTIAL_PROVIDER_FIELD_DESCRIPTOR)));
        if (!field) return E_OUTOFMEMORY;
        *field = {};
        field->dwFieldID = index;
        static constexpr CREDENTIAL_PROVIDER_FIELD_TYPE types[]{CPFT_LARGE_TEXT, CPFT_SUBMIT_BUTTON, CPFT_TILE_IMAGE, CPFT_SMALL_TEXT};
        static constexpr LPCWSTR names[]{L"JumpVNC", L"Sign in", L"JumpVNC icon", L"JumpVNC Windows sign-in"};
        field->cpft = types[index];
        if (index == Logo) field->guidFieldType = CPFG_CREDENTIAL_PROVIDER_LOGO;
        if (index == Label) field->guidFieldType = CPFG_CREDENTIAL_PROVIDER_LABEL;
        HRESULT hr = SHStrDupW(names[index], &field->pszLabel);
        if (FAILED(hr)) CoTaskMemFree(field);
        else *out = field;
        return hr;
    }
    HRESULT STDMETHODCALLTYPE GetCredentialCount(DWORD* count, DWORD* index, BOOL* autologon) override {
        if (!count || !index || !autologon) return E_POINTER;
        if (pollDuringEnumeration(scenario_, events_ != nullptr)) refresh();
        bool available = tile_ && tile_->available();
        if (available) tile_->once(Credential::Count, L"tile.counted");
        *count = available ? 1 : 0;
        *index = available ? 0 : CREDENTIAL_PROVIDER_NO_DEFAULT;
        *autologon = available;
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE GetCredentialAt(DWORD index, ICredentialProviderCredential** out) override {
        if (!out) return E_POINTER;
        *out = nullptr;
        if (index != 0 || !tile_ || !tile_->available()) return E_INVALIDARG;
        tile_->once(Credential::Enumerated, L"tile.enumerated");
        tile_->AddRef();
        *out = tile_;
        return S_OK;
    }
};

class Factory final : public IClassFactory, public Counted {
public:
    HRESULT STDMETHODCALLTYPE QueryInterface(REFIID iid, void** out) override {
        if (!out) return E_POINTER;
        *out = nullptr;
        if (iid != IID_IUnknown && iid != IID_IClassFactory) return E_NOINTERFACE;
        *out = static_cast<IClassFactory*>(this);
        AddRef();
        return S_OK;
    }
    ULONG STDMETHODCALLTYPE AddRef() override { return retain(); }
    ULONG STDMETHODCALLTYPE Release() override { return release(); }
    HRESULT STDMETHODCALLTYPE CreateInstance(IUnknown* outer, REFIID iid, void** out) override {
        if (!out) return E_POINTER;
        *out = nullptr;
        if (outer) return CLASS_E_NOAGGREGATION;
        Provider* provider = new (std::nothrow) Provider();
        if (!provider) return E_OUTOFMEMORY;
        HRESULT hr = provider->QueryInterface(iid, out);
        provider->Release();
        return hr;
    }
    HRESULT STDMETHODCALLTYPE LockServer(BOOL lock) override {
        if (lock) InterlockedIncrement(&objects);
        else InterlockedDecrement(&objects);
        return S_OK;
    }
};
}

extern "C" HRESULT WINAPI DllGetClassObject(REFCLSID clsid, REFIID iid, void** out) {
    if (!out) return E_POINTER;
    *out = nullptr;
    if (clsid != CLSID_JumpVNCLogin) return CLASS_E_CLASSNOTAVAILABLE;
    Factory* factory = new (std::nothrow) Factory();
    if (!factory) return E_OUTOFMEMORY;
    HRESULT hr = factory->QueryInterface(iid, out);
    factory->Release();
    return hr;
}

extern "C" HRESULT WINAPI DllCanUnloadNow() {
    return InterlockedCompareExchange(&objects, 0, 0) == 0 ? S_OK : S_FALSE;
}
