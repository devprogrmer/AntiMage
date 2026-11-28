import { chromium, expect } from "@playwright/test";

const origin=process.argv[2],faults=process.env.ANTIMAGE_NI_FAULT_ORIGIN;
if (!origin || !faults || [origin,faults].some(url=>new URL(url).hostname!=="127.0.0.1")) throw new Error("Disposable loopback endpoints required");
const browser=await chromium.launch({executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH});
try {
	const page=await browser.newPage();const errors=[];page.on("pageerror",err=>errors.push(err.message));
	await page.addInitScript(()=>localStorage.setItem("i18nextLng","en"));
	const login=await page.request.post(origin+"/api/auth/login",{headers:{Origin:origin},data:{username:"recovery-admin",password:"local-recovery-password"}});expect(login.status()).toBe(200);
	await page.goto(origin+"/dashboard/network-intelligence/");
	const isSnapshot=res=>new URL(res.url()).pathname.endsWith("/network-intelligence/node") && !new URL(res.url()).searchParams.has("diagnose");
	let [res]=await Promise.all([page.waitForResponse(isSnapshot),page.getByLabel("Node",{exact:true}).selectOption("7")]);
	expect(res.status()).toBe(200);expect((await res.json()).flows[0].user_id).toBe(42);
	await expect(page.getByRole("row").filter({hasText:"local-recovery"})).toHaveCount(1);
	const action=async name=>{const response=await page.request.post(faults+"/?action="+name,{headers:{Authorization:"Bearer "+process.env.ANTIMAGE_NI_FAULT_TOKEN}});expect(response.status()).toBe(204);};
	const refresh=async status=>{const [response]=await Promise.all([page.waitForResponse(res=>isSnapshot(res) && res.status()===status),page.getByRole("button",{name:"Refresh",exact:true}).click()]);return response;};
	for (const fault of ["disconnect","stall"]) {
		await action(fault);await refresh(503);
		await expect(page.getByText("Network telemetry is unavailable. Retry to check the connection.",{exact:true})).toBeVisible();
		await expect(page.getByRole("row").filter({hasText:"local-recovery"})).toHaveCount(0);
		await action("seed");res=await refresh(200);expect((await res.json()).flows[0].user_id).toBe(42);
		await expect(page.getByRole("row").filter({hasText:"local-recovery"})).toHaveCount(1);
	}
	await action("reassigned");res=await refresh(200);const reassigned=await res.json();
	expect(reassigned.flows).toHaveLength(1);expect(reassigned.flows[0].user_id).toBe(43);expect(reassigned.flows[0].upload_packets).toBe(2);expect(reassigned.flows[0].udp_quality.upload_exchange.samples).toBe(2);
	await expect(page.getByRole("row").filter({hasText:"local-recovery"}).getByText("replacement-account",{exact:true})).toBeVisible();
	await expect(page.getByRole("row").filter({hasText:"local-recovery"}).getByText("original-account",{exact:true})).toHaveCount(0);
	await action("empty");res=await refresh(200);expect((await res.json()).flows).toEqual([]);
	await expect(page.getByRole("row").filter({hasText:"local-recovery"})).toHaveCount(0);
	await action("seed");res=await refresh(200);expect((await res.json()).flows[0].user_id).toBe(42);
	await expect(page.getByRole("row").filter({hasText:"local-recovery"})).toHaveCount(1);expect(errors).toEqual([]);
	console.log(JSON.stringify({result:"PASS",actual_node_process_disconnect:true,actual_rpc_deadline:true,reconnect:true,reassignment:true,restart_without_stale_flows:true,browser_errors:errors.length,raw_wireguard:false}));
} finally {await browser.close();}
