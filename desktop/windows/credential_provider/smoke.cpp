// SPDX-License-Identifier: GPL-2.0-or-later
#include "console_login.h"
#include <cstring>
#include <cstdio>
#include <cstddef>
#include <cwchar>
#include <sddl.h>
#include <shlguid.h>
#include <wtsapi32.h>

// The Go service reads these offsets from WTSQuerySessionInformation's buffer.
static_assert(offsetof(WTSINFOEXW, Data) == 8);
static_assert(offsetof(WTSINFOEXW, Data) + offsetof(WTSINFOEX_LEVEL1_W, SessionId) == 8);
static_assert(offsetof(WTSINFOEXW, Data) + offsetof(WTSINFOEX_LEVEL1_W, SessionFlags) == 16);

#define CHECK(expr) do { if (!(expr)) { std::fprintf(stderr, "Failed at line %d\n", __LINE__); return 1; } } while (false)

// These objects live on the test stack. SIDs are made-up fixture data;
// the test never enumerates this computer's users or accesses credentials.
class SampleUser final : public ICredentialProviderUser {
    const wchar_t* sid_;
public:
    explicit SampleUser(const wchar_t* sid) : sid_(sid) {}
    ULONG refs = 1;
    HRESULT STDMETHODCALLTYPE QueryInterface(REFIID iid, void** out) override {
        if (!out) return E_POINTER;
        *out = nullptr;
        if (iid != IID_IUnknown && iid != __uuidof(ICredentialProviderUser)) return E_NOINTERFACE;
        *out = static_cast<ICredentialProviderUser*>(this);
        AddRef();
        return S_OK;
    }
    ULONG STDMETHODCALLTYPE AddRef() override { return ++refs; }
    ULONG STDMETHODCALLTYPE Release() override { return --refs; }
    HRESULT STDMETHODCALLTYPE GetSid(LPWSTR* value) override {
        const size_t bytes = (wcslen(sid_) + 1) * sizeof(wchar_t);
        *value = static_cast<PWSTR>(CoTaskMemAlloc(bytes));
        if (!*value) return E_OUTOFMEMORY;
        memcpy(*value, sid_, bytes);
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE GetProviderID(GUID*) override { return E_NOTIMPL; }
    HRESULT STDMETHODCALLTYPE GetStringValue(REFPROPERTYKEY, LPWSTR*) override { return E_NOTIMPL; }
    HRESULT STDMETHODCALLTYPE GetValue(REFPROPERTYKEY, PROPVARIANT*) override { return E_NOTIMPL; }
};

class SampleArray final : public ICredentialProviderUserArray {
public:
    ULONG refs = 1;
    DWORD count = 0;
    SampleUser* entries[2]{};
    CREDENTIAL_PROVIDER_ACCOUNT_OPTIONS options = CPAO_NONE;
    HRESULT optionsStatus = S_OK;
    HRESULT STDMETHODCALLTYPE QueryInterface(REFIID iid, void** out) override {
        if (!out) return E_POINTER;
        *out = nullptr;
        if (iid != IID_IUnknown && iid != __uuidof(ICredentialProviderUserArray)) return E_NOINTERFACE;
        *out = static_cast<ICredentialProviderUserArray*>(this);
        AddRef();
        return S_OK;
    }
    ULONG STDMETHODCALLTYPE AddRef() override { return ++refs; }
    ULONG STDMETHODCALLTYPE Release() override { return --refs; }
    HRESULT STDMETHODCALLTYPE SetProviderFilter(REFGUID) override { return E_NOTIMPL; }
    HRESULT STDMETHODCALLTYPE GetAccountOptions(CREDENTIAL_PROVIDER_ACCOUNT_OPTIONS* value) override {
        *value = options;
        return optionsStatus;
    }
    HRESULT STDMETHODCALLTYPE GetCount(DWORD* value) override { *value = count; return S_OK; }
    HRESULT STDMETHODCALLTYPE GetAt(DWORD i, ICredentialProviderUser** value) override {
        *value = nullptr;
        if (i >= count || i >= ARRAYSIZE(entries) || !entries[i]) return E_INVALIDARG;
        entries[i]->AddRef();
        *value = entries[i];
        return S_OK;
    }
};

int main() {
    using namespace console_login;
    WORD units;
    BYTE frame[12]{'J', 'V', 'C', '1', 7, 0, 0, 0, 1, 0, 'x', 0};
    CHECK(readHeader(frame, 8, 7, units) == HeaderState::Waiting);
    CHECK(readHeader(frame, 10, 7, units) == HeaderState::Waiting);
    CHECK(readHeader(frame, 12, 8, units) == HeaderState::Invalid);
    CHECK(readHeader(frame, 12, 7, units) == HeaderState::Ready && units == 1);
    frame[8] = 0;
    CHECK(readHeader(frame, 12, 7, units) == HeaderState::Invalid);
    frame[9] = 2;
    CHECK(readHeader(frame, 12, 7, units) == HeaderState::Invalid);
    Account account{};
    CHECK(!resolveLocalAccount(L"DOMAIN\\User", 11, account));
    CHECK(!resolveLocalAccount(L".\\", 2, account));
    CHECK(!resolveLocalAccount(L"X\0Y", 3, account));
    CHECK(!canSignIn(0, CPUS_LOGON, account));
    // Keep an invitation pending through the first advised unlock enumeration.
    // The timer can then announce the real zero-to-one change exactly once.
    CHECK(!pollDuringEnumeration(CPUS_UNLOCK_WORKSTATION, true));
    CHECK(pollDuringEnumeration(CPUS_UNLOCK_WORKSTATION, false));
    CHECK(pollDuringEnumeration(CPUS_LOGON, true));
    CHECK(!pollDuringEnumeration(CPUS_CREDUI, false));
    const wchar_t* target = L"S-1-5-21-1-2-3-1010";
    PSID requested;
    CHECK(ConvertStringSidToSidW(target, &requested));
    SampleUser known(target), other(L"S-1-5-21-1-2-3-1011");
    SampleArray users;
    UserTile tile = UserTile::EnumeratedUser;
    CHECK(FAILED(selectUserTile(nullptr, requested, tile)) && tile == UserTile::Hidden);
    CHECK(selectUserTile(&users, requested, tile) == S_FALSE && tile == UserTile::Hidden);
    users.options = CPAO_EMPTY_CONNECTED;
    CHECK(selectUserTile(&users, requested, tile) == S_FALSE && tile == UserTile::Hidden);
    users.options = CPAO_EMPTY_LOCAL;
    CHECK(selectUserTile(&users, requested, tile) == S_OK && tile == UserTile::OtherLocal);
    users.entries[0] = &other;
    users.entries[1] = &known;
    users.count = 2;
    CHECK(selectUserTile(&users, requested, tile) == S_OK && tile == UserTile::EnumeratedUser);
    users.options = CPAO_NONE;
    CHECK(selectUserTile(&users, requested, tile) == S_OK && tile == UserTile::EnumeratedUser);
    users.count = 1;
    CHECK(selectUserTile(&users, requested, tile) == S_FALSE && tile == UserTile::Hidden);
    users.options = CPAO_EMPTY_LOCAL;
    users.entries[0] = nullptr;
    CHECK(FAILED(selectUserTile(&users, requested, tile)) && tile == UserTile::Hidden);
    users.optionsStatus = E_FAIL;
    users.count = 0;
    CHECK(FAILED(selectUserTile(&users, requested, tile)) && tile == UserTile::Hidden);
    CHECK(known.refs == 1 && other.refs == 1);
    LocalFree(requested);
    // Load the DLL directly: testing its factory must never register a system provider.
    CHECK(SUCCEEDED(CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED)));
    HMODULE dll = LoadLibraryW(L"jumpvnc-credential-provider.dll");
    CHECK(dll);
    auto factoryFn = reinterpret_cast<HRESULT(WINAPI*)(REFCLSID, REFIID, void**)>(GetProcAddress(dll, "DllGetClassObject"));
    auto unloadFn = reinterpret_cast<HRESULT(WINAPI*)()>(GetProcAddress(dll, "DllCanUnloadNow"));
    CHECK(factoryFn && unloadFn && unloadFn() == S_OK);
    IClassFactory* factory;
    CHECK(factoryFn(CLSID_JumpVNCLogin, IID_IClassFactory, reinterpret_cast<void**>(&factory)) == S_OK);
    ICredentialProvider* provider;
    CHECK(factory->CreateInstance(nullptr, __uuidof(ICredentialProvider), reinterpret_cast<void**>(&provider)) == S_OK);
    CHECK(unloadFn() == S_FALSE);
    DWORD fieldCount = 0, logos = 0, labels = 0, submits = 0;
    CHECK(provider->GetFieldDescriptorCount(&fieldCount) == S_OK);
    for (DWORD i = 0; i < fieldCount; ++i) {
        CREDENTIAL_PROVIDER_FIELD_DESCRIPTOR* field = nullptr;
        CHECK(provider->GetFieldDescriptorAt(i, &field) == S_OK && field && field->dwFieldID == i);
        if (field->guidFieldType == CPFG_CREDENTIAL_PROVIDER_LOGO && field->cpft == CPFT_TILE_IMAGE) ++logos;
        if (field->guidFieldType == CPFG_CREDENTIAL_PROVIDER_LABEL && field->cpft == CPFT_SMALL_TEXT) ++labels;
        if (field->cpft == CPFT_SUBMIT_BUTTON) ++submits;
        CoTaskMemFree(field->pszLabel);
        CoTaskMemFree(field);
    }
    CHECK(logos == 1 && labels == 1 && submits == 1);
    HBITMAP logo = static_cast<HBITMAP>(LoadImageW(dll, MAKEINTRESOURCEW(101), IMAGE_BITMAP, 0, 0, LR_CREATEDIBSECTION));
    BITMAP details{};
    CHECK(logo && GetObjectW(logo, sizeof(details), &details) == sizeof(details));
    CHECK(details.bmWidth == 72 && details.bmHeight == 72);
    DeleteObject(logo);
    ICredentialProviderSetUserArray* receiver;
    CHECK(SUCCEEDED(provider->QueryInterface(__uuidof(ICredentialProviderSetUserArray), reinterpret_cast<void**>(&receiver))));
    CHECK(receiver->SetUserArray(&users) == S_OK && users.refs == 2);
    CHECK(receiver->SetUserArray(&users) == S_OK && users.refs == 2);
    CHECK(FAILED(receiver->SetUserArray(nullptr)) && users.refs == 1);
    receiver->Release();
    CHECK(provider->SetUsageScenario(CPUS_CREDUI, 0) == E_NOTIMPL);
    CHECK(provider->SetUsageScenario(CPUS_CHANGE_PASSWORD, 0) == E_NOTIMPL);
    DWORD count, index;
    BOOL submit;
    CHECK(provider->GetCredentialCount(&count, &index, &submit) == S_OK && !count && index == CREDENTIAL_PROVIDER_NO_DEFAULT && !submit);
    provider->Release();
    factory->Release();
    CHECK(unloadFn() == S_OK);
    FreeLibrary(dll);
    CoUninitialize();
    return 0;
}
