# Git push에서 native git 명령으로 이득 볼 수 있는 부분

## 현재 push(receive-pack) 흐름 (TCP 기준)

1. **pack 수신**  
   클라이언트 → TeeReader → **임시 .pack 파일** + **go-git packfile.Scanner**  
   - Scanner로 객체 수만큼 `NextObjectHeader()` + `NextObject(io.Discard)` 호출 → 스트림에서 pack 끝 위치까지 소비  
   - 동시에 같은 바이트를 임시 .pack에 기록

2. **persist**  
   - 임시 .pack을 열어서 `storer.PackfileWriter()`에 `io.Copy`  
   - `w.Close()` 시 go-git이 pack을 다시 읽고 **delta 적용 + .idx 생성** (여기서 CPU/디스크 부하 가능)

---

## Native git으로 이득 볼 수 있는 것

### 1. `git index-pack` (추천)

**역할:** 받은 .pack 파일 하나를 읽어서 delta 풀고, .idx를 만들고 (필요하면 thin pack 보정).

**적용 위치:** 위 2번 “persist” 단계.

- **현재:**  
  임시 .pack → go-git `PackfileWriter`에 복사 후 `Close()` → Go 쪽에서 pack 재해석 + idx 생성  
- **변경:**  
  임시 .pack을 repo 쪽으로 옮긴 뒤  
  `git -C <repo> index-pack [--fix-thin] [--stdin] ...` 로 idx 생성만 시키고,  
  go-git `PackfileWriter` 경로는 사용하지 않음.

**기대 효과**

- delta 해소·압축 해제·idx 작성이 C git으로 이전 → 대용량 push에서 수 초~수십 초 단축 가능.
- thin pack이 오면 `--fix-thin` 사용 (이미 주석에 “git index-pack --fix-thin과 유사하게” 라고 되어 있음).

**주의**

- idx/pack 최종 경로·이름을 repo의 `objects/pack/` 규칙에 맞춰야 함 (이동/이름 변경 필요할 수 있음).
- `index-pack` 실패 시 기존처럼 go-git PackfileWriter 또는 thin fallback(loose) 경로로 폴백 가능.

---

### 2. `git index-pack --stdin`

**역할:** pack 바이트를 stdin으로 받아서 .pack/.idx를 직접 씀.

**이론적 이점:**  
한 번만 읽으면서 곧바로 index-pack에 넘길 수 있음.

**현실적 제약**

- push에서는 **pack이 끝나는 시점**을 프로토콜이 별도로 알려주지 않음.
- 클라이언트는 “pack 보냄 → 서버 응답 대기”이므로, 서버가 “몇 바이트가 pack인지”를 알려면 **한 번은 스트림을 파싱**해야 함.
- 그 파싱을 go-git Scanner로 하든, 다른 방식으로 하든, “스트림 소비 + pack 끝 인지” 비용은 유지됨.
- 따라서 **“수신만 하고 파싱 없이 stdin으로 넘긴다”**는 건 프로토콜상 불가능에 가깝고,  
  **실제 이득은 “persist를 index-pack으로 대체”할 때** 나옴 (위 1번과 동일한 효과).

---

### 3. `git unpack-objects`

**역할:** pack을 풀어서 **loose 객체**만 저장.

**특징**

- pack+idx가 아니라 loose로만 쌓임 → 나중에 `git gc`/repack 필요.
- 대용량 push에서는 보통 `index-pack`이 더 빠르고, 저장소도 pack 하나로 정리됨.

**정리:**  
push 경로의 “기본 저장”은 **index-pack**으로 가는 쪽이 이득 있고,  
unpack-objects는 “thin/에러 시 loose fallback” 같은 특수 경로에만 쓰는 게 맞음.

---

## 요약 표

| 구분              | 명령/방식              | push에서 이득 가능? | 비고 |
|-------------------|------------------------|----------------------|------|
| pack 만들기       | `git pack-objects`     | ❌ (서버는 pack을 안 만듦) | fetch/clone 쪽만 해당 |
| pack 받아서 저장  | `git index-pack`        | ✅ **가능**          | persist 단계를 C git으로 대체 |
| pack 받아서 저장  | `git index-pack --stdin`| ✅ (1과 동일 효과)   | pack 끝을 알 수 있어야 함 → 한 번 파싱 필요 |
| loose만 저장      | `git unpack-objects`    | △ 제한적             | thin/에러 fallback용, 기본 경로는 index-pack 권장 |

**결론:**  
push에서 native git으로 실질적 이득을 보려면,  
**받은 pack을 go-git PackfileWriter 대신 `git index-pack`(또는 `git index-pack --stdin`)으로 처리**하도록 바꾸는 것이 맞고,  
그때 thin pack은 `--fix-thin`으로 처리하면 됨.
