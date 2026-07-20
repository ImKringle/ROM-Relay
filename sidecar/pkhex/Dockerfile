# PKHeX save-parsing sidecar. Built and run as part of the ROM Relay stack; it is
# reachable only on the internal compose network (never published to the host).
FROM mcr.microsoft.com/dotnet/sdk:10.0 AS build
WORKDIR /src
COPY pkhex.csproj .
RUN dotnet restore
COPY . .
RUN dotnet publish pkhex.csproj -c Release -o /app --no-restore

FROM mcr.microsoft.com/dotnet/aspnet:10.0
WORKDIR /app
COPY --from=build /app .
ENV ASPNETCORE_URLS=http://0.0.0.0:8081
ENV ASPNETCORE_ENVIRONMENT=Production
EXPOSE 8081
ENTRYPOINT ["dotnet", "pkhex.dll"]
