import {
	Alert,
	AlertIcon,
	Box,
	Heading,
	Spinner,
	Stack,
	Text,
} from "@chakra-ui/react";
import { ArchiveBoxIcon } from "@heroicons/react/24/outline";
import { DashboardBackupControls } from "components/AntiMageBackupPanel";
import { ChartBox } from "components/common/ChartBox";
import useGetUser from "hooks/useGetUser";
import { useTranslation } from "react-i18next";
import { useQuery } from "react-query";
import { fetch } from "service/http";
import { AdminRole } from "types/Admin";

type MaintenanceInfo = {
	panel?: {
		mode?: string;
		install_mode?: string;
	} | null;
};

export const BackupRestorePage = () => {
	const { t } = useTranslation();
	const { userData, getUserIsSuccess, getUserIsPending } = useGetUser();
	const canBackUp =
		userData.role === AdminRole.FullAccess ||
		(userData.role === AdminRole.Sudo &&
			Boolean(userData.permissions?.sudo?.backups));
	const runtime = useQuery<MaintenanceInfo>(
		["backup-runtime-info"],
		() => fetch<MaintenanceInfo>("/maintenance/info", { timeout: 8000 }),
		{
			enabled: getUserIsSuccess && canBackUp,
			refetchOnWindowFocus: false,
			retry: false,
		},
	);
	const isBinaryRuntime =
		(runtime.data?.panel?.mode || runtime.data?.panel?.install_mode) ===
		"binary";

	return (
		<Stack className="am-dashboard" spacing={5} width="full">
			<Box className="am-dashboard-hero">
				<Text className="am-dashboard-eyebrow">
					AntiMage / {t("settings.backup.tabTitle")}
				</Text>
				<Heading className="am-dashboard-title" size="lg">
					{t("settings.backup.pageTitle")}
				</Heading>
				<Text className="am-dashboard-description">
					{t("settings.backup.pageDescription")}
				</Text>
			</Box>
			{getUserIsPending ? (
				<ChartBox title={t("settings.backup.pageTitle")}>
					<Stack align="center" py={8}>
						<Spinner />
					</Stack>
				</ChartBox>
			) : getUserIsSuccess && canBackUp ? (
				<ChartBox title={t("settings.backup.title")}>
					<Stack spacing={4}>
						<DashboardBackupControls
							isBinaryRuntime={isBinaryRuntime}
							runtimeLoading={runtime.isLoading || runtime.isFetching}
						/>
						{runtime.isSuccess && !isBinaryRuntime && (
							<Alert status="warning" borderRadius="md">
								<AlertIcon />
								<Text>{t("settings.backup.binaryOnlyNotice")}</Text>
							</Alert>
						)}
						{runtime.isError && (
							<Alert status="error" borderRadius="md">
								<AlertIcon />
								<Text>{t("settings.backup.runtimeCheckFailed")}</Text>
							</Alert>
						)}
					</Stack>
				</ChartBox>
			) : (
				<ChartBox title={t("settings.backup.pageTitle")}>
					<Stack align="center" py={8} spacing={3}>
						<ArchiveBoxIcon width={32} height={32} />
						<Text>{t("settings.backup.noPermission")}</Text>
					</Stack>
				</ChartBox>
			)}
		</Stack>
	);
};
